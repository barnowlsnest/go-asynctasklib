package workerpool

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// ErrInvalidPool returned by New when required options are missing or
// the supplied Config is unusable (nil handler, nil config, unknown Mode).
// It is always joined with a second error explaining the specific failure.
var ErrInvalidPool = errors.New("invalid pool configuration")

// Mode selects the pool's scaling strategy: ModeFixedSize keeps a constant
// worker count, ModeAutoScale varies it between MinSize and MaxSize with load.
type Mode int

const (
	// ModeFixedSize keeps all configured workers subscribed for the entire
	// pool lifetime. Worker count never changes after New returns.
	ModeFixedSize Mode = iota
	// ModeAutoScale keeps the number of joined workers between
	// AutoScaleConfig.MinSize and AutoScaleConfig.MaxSize based on load.
	ModeAutoScale
)

const (
	defaultRate    = 750
	defaultBacklog = 1000
)

const (
	defaultScaleUpStep         = 1
	defaultScalerInterval      = 100 * time.Millisecond
	defaultIdleHeadroom        = 1
	defaultScaleDownCooldown   = 5 * time.Second
	defaultScaleDownIdlePeriod = 2 * time.Second
)

type (
	// PoolEvents extends Events with DispatchError, the callback invoked
	// when the claims dispatcher fails to hand a job to any worker
	// (typically because SubmitTimeout elapsed with no accepting worker).
	// Embed NoopEvents to satisfy PoolEvents without implementing every
	// hook.
	PoolEvents[T any] interface {
		Events[T]
		DispatchError(error, T)
	}

	// WorkerPool is a generic pool of worker goroutines — fixed-size
	// (ModeFixedSize) or auto-scaling between MinSize and MaxSize
	// (ModeAutoScale) — that dispatches submitted jobs via a claims-based
	// rendezvous channel. Construct with New; drive with Submit; tear down with
	// Shutdown or GracefulShutdown. All exported methods are safe for
	// concurrent use.
	WorkerPool[T any] struct {
		onceClose        sync.Once
		err              atomic.Pointer[error]
		reject           atomic.Bool
		mu               sync.Mutex
		shutdown         chan struct{}
		drained          chan struct{}
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
		scalerDone       chan struct{}    // closed when runScaler exits; nil in fixed mode
		tickSource       <-chan time.Time // injected scaler tick (tests); nil -> real ticker
		nowFn            func() int64     // injected clock (tests); nil -> time.Now().UnixNano
	}

	// Config aggregates the tunables that shape a WorkerPool's runtime
	// behavior. Pass a pointer to New via WithConfig; zero or negative
	// numeric fields are replaced with defaults (see applyDefaults).
	// ClaimsConfig is embedded so its fields (Size, SubmitTimeout, Name)
	// can be set directly on the same literal.
	Config struct {
		ClaimsConfig
		// Mode selects the pool's scaling strategy. ModeFixedSize (the
		// default) keeps a constant worker count; ModeAutoScale varies it
		// between AutoScale.MinSize and AutoScale.MaxSize with load.
		Mode Mode
		// RateLimit caps submissions per second accepted into the backlog.
		// Defaults to 750 when unset. Enforced by a token-bucket limiter
		// with burst equal to ClaimsConfig.Size.
		RateLimit float64
		// Backlog is the buffered size of the internal job queue.
		// Defaults to 1000 when unset. Submit blocks on a full backlog
		// up to ClaimsConfig.SubmitTimeout before returning
		// ErrSubmitTimeout.
		Backlog int
		// AutoScale tunes ModeAutoScale. It is ignored unless Mode is
		// ModeAutoScale. In auto mode MaxSize is the ceiling: it sizes the
		// claims buffer, the subscriber cap, and the rate-limiter burst, and
		// ClaimsConfig.Size is ignored.
		AutoScale AutoScaleConfig
	}

	// PoolOptionFunc configures a WorkerPool during New. Helpers like
	// WithConfig, WithHandler, and WithEvents return instances; callers
	// rarely need to write their own.
	PoolOptionFunc[T any] func(*WorkerPool[T])

	// AutoScaleConfig tunes ModeAutoScale. Zero or negative numeric fields
	// are replaced with defaults by applyDefaults; the only hard error is
	// MinSize greater than MaxSize.
	AutoScaleConfig struct {
		// MinSize is the floor of joined workers. The pool never drops below
		// it except during shutdown. Defaults to 1.
		MinSize int
		// MaxSize is the ceiling of joined workers and sizes the claims
		// buffer, subscriber cap, and rate burst. Defaults to runtime.NumCPU.
		MaxSize int
		// ScaleUpStep is the number of workers added per up-decision.
		// Defaults to 1.
		ScaleUpStep int
		// IdleHeadroom is the number of spare idle workers the pool tries to
		// keep. Defaults to 1.
		IdleHeadroom int
		// Interval is the scaler tick period. Defaults to 100ms.
		Interval time.Duration
		// ScaleUpCooldown is the minimum gap between up-decisions. Defaults
		// to 0 (may scale up every tick).
		ScaleUpCooldown time.Duration
		// ScaleDownCooldown is the minimum gap between down-decisions.
		// Defaults to 5s.
		ScaleDownCooldown time.Duration
		// ScaleDownIdlePeriod is how long a worker must be idle before it is
		// eligible for removal. Defaults to 2s; tune to >= 2x handler p99.
		ScaleDownIdlePeriod time.Duration
	}
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

func (cfg *AutoScaleConfig) applyDefaults() {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = runtime.NumCPU()
	}
	if cfg.MinSize <= 0 {
		cfg.MinSize = 1
	}
	if cfg.ScaleUpStep <= 0 {
		cfg.ScaleUpStep = defaultScaleUpStep
	}
	if cfg.IdleHeadroom <= 0 {
		cfg.IdleHeadroom = defaultIdleHeadroom
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultScalerInterval
	}
	if cfg.ScaleUpCooldown < 0 {
		cfg.ScaleUpCooldown = 0
	}
	if cfg.ScaleDownCooldown <= 0 {
		cfg.ScaleDownCooldown = defaultScaleDownCooldown
	}
	if cfg.ScaleDownIdlePeriod <= 0 {
		cfg.ScaleDownIdlePeriod = defaultScaleDownIdlePeriod
	}
}

func (cfg *AutoScaleConfig) validate() error {
	if cfg.MinSize > cfg.MaxSize {
		return fmt.Errorf("MinSize %d exceeds MaxSize %d", cfg.MinSize, cfg.MaxSize)
	}
	return nil
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

// withScalerClock injects the scaler's tick source and clock for deterministic
// tests. Production code never sets these; New falls back to a real time.Ticker
// and time.Now when they are nil.
func withScalerClock[T any](tick <-chan time.Time, now func() int64) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.tickSource = tick
		pool.nowFn = now
	}
}

// New constructs a WorkerPool bound to ctx. It applies the given options,
// validates the configuration, creates the worker set, subscribes the initial
// workers to the claims dispatcher, and starts the dispatch loop. In
// ModeFixedSize all workers are subscribed; in ModeAutoScale only MinSize are,
// and New also starts the scaler goroutine that adjusts the count with load.
// The returned pool is ready to accept Submit calls. Canceling ctx, calling
// Shutdown, or calling GracefulShutdown tears the pool down.
//
// WithConfig and WithHandler are required. New returns ErrInvalidPool
// joined with a descriptive error when a required option is missing, the
// Mode is unrecognized, or the ClaimsConfig is invalid. If ctx is already
// canceled, New returns ctx.Err() instead.
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
		shutdown:  make(chan struct{}),
		drained:   make(chan struct{}),
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

	if pool.cfg.Mode == ModeAutoScale {
		pool.cfg.AutoScale.applyDefaults()
		if err := pool.cfg.AutoScale.validate(); err != nil {
			cancel()
			return nil, errors.Join(ErrInvalidPool, err)
		}
		// MaxSize is the ceiling: size the claims buffer, subscriber cap, and
		// rate burst to it. ClaimsConfig.Size is ignored in auto mode.
		pool.cfg.Size = pool.cfg.AutoScale.MaxSize
	}

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
	case ModeAutoScale:
		initialJoin = pool.cfg.AutoScale.MinSize
	default:
		cancel()
		return nil, errors.Join(ErrInvalidPool, errors.New("invalid mode"))
	}

	if err := pool.joinInitial(initialJoin); err != nil {
		cancel()
		return nil, err
	}

	go pool.dispatcher()

	if pool.cfg.Mode == ModeAutoScale {
		pool.startScaler()
	}

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

// startScaler spawns the autoscaler goroutine. It uses the injected tick source
// and clock when present (tests) and otherwise a real time.Ticker and time.Now.
// The ticker is stopped when the scaler returns.
func (pool *WorkerPool[T]) startScaler() {
	pool.scalerDone = make(chan struct{})

	now := pool.nowFn
	if now == nil {
		now = func() int64 { return time.Now().UnixNano() }
	}

	if pool.tickSource != nil {
		go pool.runScaler(pool.tickSource, now)
		return
	}

	ticker := time.NewTicker(pool.cfg.AutoScale.Interval)
	go func() {
		defer ticker.Stop()
		pool.runScaler(ticker.C, now)
	}()
}

// Shutdown cancels the pool context immediately and returns once the
// dispatcher has exited. In-flight handlers observe cancellation through
// their JobAware argument; jobs still sitting in the backlog are dropped.
// Subsequent Submit calls return ErrPoolShutdown. Safe to call multiple
// times and from multiple goroutines — only the first call does work.
func (pool *WorkerPool[T]) Shutdown() {
	pool.close(false)
}

// GracefulShutdown stops accepting new submissions and waits for the
// dispatcher to hand off every job already buffered in the backlog before
// returning. Handlers that are running when GracefulShutdown is called
// run to completion. Subsequent Submit calls return ErrPoolShutdown.
// Safe to call multiple times and from multiple goroutines — only the
// first call does work.
func (pool *WorkerPool[T]) GracefulShutdown() {
	pool.close(true)
}

func (pool *WorkerPool[T]) close(shouldDrain bool) {
	pool.onceClose.Do(func() {
		defer pool.cancel()
		pool.reject.Store(true)
		close(pool.shutdown)
		if !shouldDrain {
			pool.cancel()
		}

		<-pool.drained
		if pool.cfg.Mode == ModeAutoScale {
			<-pool.scalerDone
		}
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

// Submit enqueues job onto the pool's backlog, using ctx as the caller
// context that will be merged with the pool context and handed to the
// handler via its JobAware argument. Canceling ctx before the job is
// picked up aborts the submission; canceling it after pickup surfaces
// to the handler as ctx.Done() / ctx.Err().
//
// Submit first waits on the rate limiter, then attempts to place the job
// on the backlog channel. It returns:
//
//   - ErrNilCtx if ctx is nil.
//   - ErrPoolShutdown if Shutdown or GracefulShutdown was called.
//   - ctx.Err() if the pool context is canceled while waiting.
//   - ErrSubmitTimeout if the backlog stays full for longer than
//     ClaimsConfig.SubmitTimeout.
//   - nil once the job is accepted onto the backlog. A nil return does
//     not guarantee the handler ran — GracefulShutdown drains, Shutdown
//     does not.
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
		return pool.ctx().Err()
	case <-pool.shutdown:
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
	defer close(pool.drained)
	for {
		select {
		case <-pool.ctx().Done():
			return
		case <-pool.shutdown:
			if len(pool.backlog) == 0 {
				return
			}
		case ctx := <-pool.backlog:
			if err := pool.availableWorkers.submit(ctx); err != nil {
				pool.events.DispatchError(err, ctx.Job())
			}
		}
	}
}
