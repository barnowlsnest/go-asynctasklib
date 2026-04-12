package workerpool

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidPool = errors.New("invalid pool configuration")

type Mode int

const (
	ModeFixedSize Mode = iota
)

const (
	defaultRate    = 750
	defaultBacklog = 1000
)

type (
	PoolEvents[T any] interface {
		Events[T]
		DispatchError(error, T)
	}

	WorkerPool[T any] struct {
		onceClose        sync.Once
		err              atomic.Pointer[error]
		reject           atomic.Bool
		mu               sync.Mutex
		backlog          chan JobAware[T]
		workers          []*worker[T]
		joinedIDs        map[uint64]struct{}
		ctx              func() context.Context
		cancel           context.CancelFunc
		limiter          *rate.Limiter
		cfg              *Config
		availableWorkers *claims[T]
		handler          HandlerFunc[T]
		events           PoolEvents[T]
	}

	Config struct {
		ClaimsConfig
		// Mode selects FixedSize or AutoScale. Defaults to FixedSize.
		Mode Mode
		// RateLimit caps submissions per second accepted into the backlog.
		RateLimit float64
		// Backlog is the buffered size of the internal job queue.
		Backlog int
	}

	PoolOptionFunc[T any] func(*WorkerPool[T])
)

func (cfg *Config) applyDefaults() {
	cfg.ClaimsConfig.applyDefaults()

	if cfg.RateLimit <= 0 {
		cfg.RateLimit = defaultRate
	}
	if cfg.Backlog <= 0 {
		cfg.Backlog = defaultBacklog
	}
}

// WithConfig sets the pool's Config. It is required: New returns
// ErrInvalidPool if no Config is provided.
func WithConfig[T any](cfg *Config) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.cfg = cfg
	}
}

// WithHandler sets the per-job handler invoked by every worker. It is
// required: New returns ErrInvalidPool if no handler is provided.
func WithHandler[T any](handler HandlerFunc[T]) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.handler = handler
	}
}

// WithEvents sets the lifecycle observer for the pool. If omitted, the
// pool installs NoopEvents so handler code never has to nil-check.
func WithEvents[T any](events PoolEvents[T]) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.events = events
	}
}

// New constructs a WorkerPool bound to ctx. It applies the given options,
// validates the configuration, creates all workers, Joins the initial set
// (all of them in ModeFixedSize, MinSize of them in ModeAutoScale), and
// starts the dispatch loop. The returned pool is ready to accept Submit
// calls. Canceling ctx or calling Close drains the pool.
//
// New returns ErrInvalidPool when a required option is missing, or the
// context's error if ctx is already canceled.
func New[T any](ctx context.Context, opts ...PoolOptionFunc[T]) (*WorkerPool[T], error) {
	if ctx == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil context"))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	poolCtx, cancel := context.WithCancel(ctx)
	pool := &WorkerPool[T]{
		ctx:       func() context.Context { return poolCtx },
		cancel:    cancel,
		joinedIDs: make(map[uint64]struct{}),
	}

	for _, opt := range opts {
		opt(pool)
	}

	switch {
	case pool.handler == nil:
		cancel()
		return nil, errors.Join(ErrInvalidPool, errors.New("nil handler"))
	case pool.cfg == nil:
		cancel()
		return nil, errors.Join(ErrInvalidPool, errors.New("nil config"))
	}

	pool.cfg.applyDefaults()
	workClaims, err := newClaims[T](&pool.cfg.ClaimsConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	pool.availableWorkers = workClaims
	pool.backlog = make(chan JobAware[T], pool.cfg.Backlog)
	pool.limiter = rate.NewLimiter(rate.Limit(pool.cfg.RateLimit), pool.cfg.Size)
	if err = pool.createWorkers(pool.cfg.Size); err != nil {
		cancel()
		return nil, err
	}

	var initialJoin int
	switch pool.cfg.Mode {
	case ModeFixedSize:
		initialJoin = pool.cfg.Size
	default:
		return nil, errors.Join(ErrInvalidPool, errors.New("invalid mode"))
	}

	if err := pool.joinInitial(initialJoin); err != nil {
		cancel()
		return nil, err
	}

	go pool.dispatcher()

	return pool, nil
}

func (pool *WorkerPool[T]) createWorkers(n int) error {
	pool.workers = make([]*worker[T], 0, n)
	for i := range n {
		w, err := newWorker(&workerConfig[T]{
			ID:          uint64(i + 1),
			HandlerFunc: pool.handler,
			Events:      pool.events,
		})
		if err != nil {
			return err
		}
		pool.workers = append(pool.workers, w)
	}

	return nil
}

func (pool *WorkerPool[T]) joinInitial(n int) error {
	n = min(n, len(pool.workers))
	for i := range n {
		w := pool.workers[i]
		if err := w.join(pool.ctx(), pool.availableWorkers); err != nil {
			return err
		}
		pool.mu.Lock()
		pool.joinedIDs[w.ID()] = struct{}{}
		pool.mu.Unlock()
	}
	return nil
}

func (pool *WorkerPool[T]) Shutdown() {
	pool.close(false)
}

func (pool *WorkerPool[T]) GracefulShutdown() {
	pool.close(true)
}

func (pool *WorkerPool[T]) close(shouldDrain bool) {
	pool.onceClose.Do(func() {
		defer pool.cancel()
		pool.reject.Store(true)
		if shouldDrain {
			for {
				if len(pool.backlog) > 0 {
					runtime.Gosched()
				} else {
					break
				}
			}
		}

		close(pool.backlog)
		pool.mu.Lock()
		joined := make([]*worker[T], 0, len(pool.joinedIDs))
		for _, w := range pool.workers {
			if _, ok := pool.joinedIDs[w.ID()]; ok {
				joined = append(joined, w)
			}
		}
		pool.joinedIDs = nil
		pool.mu.Unlock()

		for _, w := range joined {
			_ = w.leave(pool.availableWorkers, pool.cfg.SubmitTimeout)
		}
	})
}

// Submit an enqueues job onto the pool's backlog. It is a convenience wrapper
// around [WorkerPool.SubmitContext] with a background caller context, and
// exists so callers who do not need caller-side cancellation can avoid the
// ceremony of threading a context.
//
// See [WorkerPool.SubmitContext] for the full list of return values and
// cancellation semantics.
func (pool *WorkerPool[T]) Submit(ctx context.Context, job T) error {
	switch {
	case ctx == nil:
		return ErrNilCtx
	case pool.reject.Load():
		return ErrPoolShutdown
	}

	if err := pool.limiter.Wait(pool.ctx()); err != nil {
		return err
	}

	select {
	case <-pool.ctx().Done():
		return ErrPoolShutdown
	case pool.backlog <- newJobContext[T](pool.ctx(), ctx, job):
		return nil
	case <-time.After(pool.cfg.SubmitTimeout):
		return ErrSubmitTimeout
	}
}

// Error returns the last fatal dispatch error observed by the pool, or nil
// if there is none. Transient errors such as ErrSubmitTimeout (in
// ModeFixedSize) are swallowed and do not surface here.
func (pool *WorkerPool[T]) Error() error {
	if err := pool.err.Load(); err != nil {
		return *err
	}

	return nil
}

// JoinedCount returns the number of workers currently subscribed to the
// claims dispatcher. In FixedSize mode this is constant; in AutoScale mode
// it moves between MinSize and MaxSize based on demand.
func (pool *WorkerPool[T]) JoinedCount() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.joinedIDs)
}

func (pool *WorkerPool[T]) dispatcher() {
	for {
		select {
		case <-pool.ctx().Done():
			return
		case ctx, ok := <-pool.backlog:
			if !ok {
				return
			}

			if err := pool.availableWorkers.submit(ctx); err != nil {
				pool.events.DispatchError(err, ctx.Job())
			}
		}
	}
}
