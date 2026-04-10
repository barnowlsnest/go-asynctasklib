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

// ErrInvalidPool is returned from New when the pool cannot be constructed
// because a required option is missing or malformed.
var ErrInvalidPool = errors.New("invalid pool configuration")

// Mode selects how the pool manages its workers' Claims membership.
//
//   - ModeFixedSize creates Size workers and Joins them all on startup.
//     The set of subscribed workers is constant for the pool's lifetime.
//
//   - ModeAutoScale creates MaxSize workers but only Joins MinSize of them
//     on startup. A scaler goroutine watches the backlog and Joins more
//     workers (up to MaxSize) when jobs queue up, and Leaves idle workers
//     (down to MinSize) once they have been idle for IdleTimeout.
type Mode int

const (
	ModeFixedSize Mode = iota
	ModeAutoScale
)

const (
	defaultRate               = 750
	defaultBacklog            = 1000
	defaultIdleLeaveThreshold = 5 * time.Second
	defaultScaleInterval      = 100 * time.Millisecond
	maxDispatchRetries        = 5
)

type (
	// WorkerPool is a generic worker pool that dispatches jobs of type T
	// through a Claims-based fan-out. Construct it with New and shut it
	// down with Close. The zero value is not usable.
	WorkerPool[T any] struct {
		once             sync.Once
		wg               sync.WaitGroup
		err              atomic.Pointer[error]
		reject           atomic.Bool
		mu               sync.Mutex
		backlog          chan *T
		workers          []*Worker[T]
		joinedIDs        map[uint64]struct{}
		scaleReq         chan struct{}
		ctx              func() context.Context
		cancel           context.CancelFunc
		limiter          *rate.Limiter
		cfg              *Config
		availableWorkers *Claims[T]
		handler          HandlerFunc[T]
		events           Events[T]
	}

	// Config controls pool construction. Fields under ClaimsConfig drive the
	// underlying Claims dispatcher; the top-level fields add pool-specific
	// behavior such as mode selection, backlog size, and rate limiting.
	Config struct {
		ClaimsConfig
		// Mode selects FixedSize or AutoScale. Defaults to FixedSize.
		Mode Mode
		// MinSize is the minimum number of workers kept subscribed while in
		// AutoScale mode. Ignored in FixedSize mode.
		MinSize int
		// MaxSize is the upper bound on Joined workers in AutoScale mode.
		// If zero, falls back to ClaimsConfig.Size, then runtime.NumCPU().
		// Ignored in FixedSize mode.
		MaxSize int
		// IdleTimeout is how long a worker may stay idle (no jobs processed)
		// before the autoscaler Leaves it. Ignored in FixedSize mode.
		// Defaults to 5s.
		IdleTimeout time.Duration
		// ScaleInterval is the period at which the autoscaler evaluates
		// whether to scale up or down. Defaults to 100ms.
		ScaleInterval time.Duration
		// RateLimit caps submissions per second accepted into the backlog.
		RateLimit float64
		// Backlog is the buffered size of the internal job queue.
		Backlog int
	}

	// PoolOptionFunc is the functional option signature accepted by New.
	// See WithConfig, WithHandler, and WithEvents for the built-in options.
	PoolOptionFunc[T any] func(*WorkerPool[T])
)

func (cfg *Config) applyDefaults() {
	if cfg.Mode == ModeAutoScale {
		if cfg.MaxSize <= 0 {
			cfg.MaxSize = cfg.Size
		}
		if cfg.MaxSize <= 0 {
			cfg.MaxSize = runtime.NumCPU()
		}
		if cfg.MinSize < 0 {
			cfg.MinSize = 0
		}
		if cfg.MinSize > cfg.MaxSize {
			cfg.MinSize = cfg.MaxSize
		}
		// The Claims dispatcher must have room for every worker that may
		// ever Subscribe, so its Size mirrors MaxSize in autoscale mode.
		cfg.Size = cfg.MaxSize
		if cfg.IdleTimeout <= 0 {
			cfg.IdleTimeout = defaultIdleLeaveThreshold
		}
		if cfg.ScaleInterval <= 0 {
			cfg.ScaleInterval = defaultScaleInterval
		}
	}

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
func WithEvents[T any](events Events[T]) PoolOptionFunc[T] {
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
		scaleReq:  make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(pool)
	}

	if pool.handler == nil {
		cancel()
		return nil, errors.Join(ErrInvalidPool, errors.New("nil handler"))
	}

	if pool.cfg == nil {
		cancel()
		return nil, errors.Join(ErrInvalidPool, errors.New("nil config"))
	}

	if pool.events == nil {
		pool.events = NewNoopEvents[T]()
	}

	pool.cfg.applyDefaults()

	workClaims, err := NewClaims[T](&pool.cfg.ClaimsConfig)
	if err != nil {
		cancel()
		return nil, err
	}

	pool.availableWorkers = workClaims
	pool.backlog = make(chan *T, pool.cfg.Backlog)
	pool.limiter = rate.NewLimiter(rate.Limit(pool.cfg.RateLimit), pool.cfg.Size)

	if err := pool.createWorkers(pool.cfg.Size); err != nil {
		cancel()
		return nil, err
	}

	initialJoin := pool.cfg.Size
	if pool.cfg.Mode == ModeAutoScale {
		initialJoin = pool.cfg.MinSize
	}
	if err := pool.joinInitial(initialJoin); err != nil {
		cancel()
		return nil, err
	}

	pool.wg.Go(pool.listen)
	if pool.cfg.Mode == ModeAutoScale {
		pool.wg.Go(pool.runScaler)
	}

	return pool, nil
}

func (pool *WorkerPool[T]) createWorkers(n int) error {
	pool.workers = make([]*Worker[T], 0, n)
	for i := range n {
		w, err := NewWorker(&WorkerConfig[T]{
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
		if err := w.Join(pool.ctx(), pool.availableWorkers); err != nil {
			return err
		}
		pool.mu.Lock()
		pool.joinedIDs[w.ID()] = struct{}{}
		pool.mu.Unlock()
	}
	return nil
}

// Close shuts the pool down. It is idempotent (safe to call multiple
// times) and non-blocking after the first call returns: it flips the
// pool into rejecting mode, cancels the internal context to unblock
// in-flight workers, waits for the dispatch loop and scaler to exit,
// then Leaves every subscribed worker.
//
// Close does not drain the backlog: jobs still queued when Close is
// called are dropped. Submit called after Close returns ErrPoolShutdown.
func (pool *WorkerPool[T]) Close() {
	pool.once.Do(func() {
		pool.reject.Store(true)
		pool.cancel()
		pool.wg.Wait()

		pool.mu.Lock()
		joined := make([]*Worker[T], 0, len(pool.joinedIDs))
		for _, w := range pool.workers {
			if _, ok := pool.joinedIDs[w.ID()]; ok {
				joined = append(joined, w)
			}
		}
		pool.joinedIDs = nil
		pool.mu.Unlock()

		for _, w := range joined {
			_ = w.Leave(pool.availableWorkers, pool.cfg.SubmitTimeout)
		}

		// Intentionally do not close(pool.backlog): callers may still be
		// inside Submit after reject flipped, and closing would race with
		// an in-flight chan send. listen() already exits on ctx cancel,
		// and GC reclaims the channel when the pool is dropped.
	})
}

// Submit enqueues job onto the pool's backlog. It blocks until space is
// available, the pool context is canceled, or Config.SubmitTimeout elapses.
//
// Submit returns:
//   - ErrNilJob if job is nil
//   - ErrPoolShutdown if Close has already been called
//   - ErrSubmitTimeout if the backlog stays full for longer than SubmitTimeout
//   - the context error if the pool context is canceled while waiting
//
// Once a job is on the backlog, delivery to a worker is asynchronous and
// observable only via the Events hooks.
func (pool *WorkerPool[T]) Submit(job *T) error {
	if job == nil {
		return ErrNilJob
	}

	if pool.reject.Load() {
		return ErrPoolShutdown
	}

	if err := pool.limiter.Wait(pool.ctx()); err != nil {
		return err
	}

	select {
	case <-pool.ctx().Done():
		return pool.ctx().Err()
	case pool.backlog <- job:
		return nil
	case <-time.After(pool.cfg.SubmitTimeout):
		return ErrSubmitTimeout
	}
}

// Err returns the last fatal dispatch error observed by the pool, or nil
// if there is none. Transient errors such as ErrSubmitTimeout (in
// ModeFixedSize) are swallowed and do not surface here.
func (pool *WorkerPool[T]) Err() error {
	if err := pool.err.Load(); err != nil {
		return *err
	}
	return nil
}

// JoinedCount returns the number of workers currently subscribed to the
// Claims dispatcher. In FixedSize mode this is constant; in AutoScale mode
// it moves between MinSize and MaxSize based on demand.
func (pool *WorkerPool[T]) JoinedCount() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.joinedIDs)
}

func (pool *WorkerPool[T]) listen() {
	for {
		select {
		case <-pool.ctx().Done():
			return
		case job, ok := <-pool.backlog:
			if !ok {
				return
			}
			pool.dispatch(job)
		}
	}
}

// dispatch hands a job off to the Claims dispatcher. In AutoScale mode it
// retries a bounded number of times on ErrSubmitTimeout / ErrNoWorkers,
// nudging the scaler so it can spin up additional workers while we wait.
func (pool *WorkerPool[T]) dispatch(job *T) {
	for attempt := 0; ; attempt++ {
		if err := pool.ctx().Err(); err != nil {
			return
		}

		err := pool.availableWorkers.Submit(pool.ctx(), job)
		if err == nil {
			return
		}

		retriable := errors.Is(err, ErrSubmitTimeout) || errors.Is(err, ErrNoWorkers)
		if pool.cfg.Mode == ModeAutoScale && retriable && attempt < maxDispatchRetries {
			pool.requestScale()
			select {
			case <-pool.ctx().Done():
				return
			case <-time.After(pool.cfg.ScaleInterval):
			}
			continue
		}

		if errors.Is(err, ErrSubmitTimeout) {
			// Preserve the drop-on-timeout behavior for FixedSize mode: the
			// pool keeps running, the job is lost.
			return
		}

		pool.err.Store(&err)
		return
	}
}
