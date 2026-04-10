package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidPool = errors.New("invalid pool configuration")

const (
	defaultRate    = 750
	defaultBacklog = 1000
)

type (
	WorkerPool[T any] struct {
		once             sync.Once
		wg               sync.WaitGroup
		err              atomic.Pointer[error]
		reject           atomic.Bool
		backlog          chan *T
		workers          map[uint64]*Worker[T]
		ctx              func() context.Context
		cancel           context.CancelFunc
		limiter          *rate.Limiter
		cfg              *Config
		availableWorkers *Claims[T]
		handler          HandlerFunc[T]
		events           Events[T]
	}

	Config struct {
		ClaimsConfig
		RateLimit   float64
		Backlog     int
		IdleTimeout time.Duration
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

func WithConfig[T any](cfg *Config) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.cfg = cfg
	}
}

func WithHandler[T any](handler HandlerFunc[T]) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.handler = handler
	}
}

func WithEvents[T any](events Events[T]) PoolOptionFunc[T] {
	return func(pool *WorkerPool[T]) {
		pool.events = events
	}
}

func New[T any](ctx context.Context, opts ...PoolOptionFunc[T]) (*WorkerPool[T], error) {
	if ctx == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil context"))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	poolCtx, cancel := context.WithCancel(ctx)
	pool := &WorkerPool[T]{
		ctx:    func() context.Context { return poolCtx },
		cancel: cancel,
	}

	for _, opt := range opts {
		opt(pool)
	}

	if pool.handler == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil handler"))
	}

	if pool.cfg == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil config"))
	}

	if pool.events == nil {
		pool.events = NewNoopEvents[T]()
	}

	pool.cfg.applyDefaults()
	workClaims, err := NewClaims[T](&pool.cfg.ClaimsConfig)
	if err != nil {
		return nil, err
	}

	pool.availableWorkers = workClaims
	pool.workers = make(map[uint64]*Worker[T], pool.cfg.Size)
	pool.backlog = make(chan *T, pool.cfg.Backlog)
	pool.limiter = rate.NewLimiter(rate.Limit(pool.cfg.RateLimit), pool.cfg.Size)

	pool.wg.Go(pool.listen)

	return pool, nil
}

func (pool *WorkerPool[T]) init() error {
	var err error
	for i := range pool.cfg.Size {
		if pool.workers[uint64(i+1)], err = NewWorker[T](&WorkerConfig[T]{}); err != nil {
			return err
		}
	}

	return nil
}

func (pool *WorkerPool[T]) Close() {
	pool.once.Do(func() {
		defer close(pool.backlog)
		pool.reject.Store(true)
		pool.cancel()
		pool.wg.Wait()
	})
}

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

func (pool *WorkerPool[T]) Err() error {
	err := pool.err.Load()
	if err != nil {
		return *err
	}

	return nil
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

			if err := pool.availableWorkers.Submit(pool.ctx(), job); err != nil {
				switch {
				case errors.Is(err, ErrSubmitTimeout):
					continue
				default:
					pool.err.Store(&err)
					return
				}
			}
		}
	}
}
