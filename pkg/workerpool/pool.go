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

type (
	WorkerPool[T any] struct {
		once    sync.Once
		wg      sync.WaitGroup
		reject  atomic.Bool
		backlog chan *T
		ctx     func() context.Context
		cancel  context.CancelFunc
		limiter *rate.Limiter
		cfg     *Config
		workers *Claims[T]
	}

	Config struct {
		RateLimit     float64
		SubmitTimeout time.Duration
		Backlog       int
		Size          int
	}
)

func New[T any](ctx context.Context, workers *Claims[T], cfg *Config) (*WorkerPool[T], error) {
	if cfg == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil config"))
	}

	if ctx == nil {
		return nil, errors.Join(ErrInvalidPool, errors.New("nil context"))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	poolCtx, cancel := context.WithCancel(ctx)
	pool := &WorkerPool[T]{
		workers: workers,
		cfg:     cfg,
		backlog: make(chan *T, cfg.Backlog),
		ctx:     func() context.Context { return poolCtx },
		cancel:  cancel,
		limiter: rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.Size),
	}

	pool.wg.Go(pool.listen)

	return pool, nil
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

func (pool *WorkerPool[T]) listen() {
	for {
		select {
		case <-pool.ctx().Done():
			return
		case job, ok := <-pool.backlog:
			if !ok {
				return
			}

			if err := pool.submit(job); err != nil {
				switch {
				case errors.Is(err, ErrDispatcherClosed):
					return
				case errors.Is(err, ErrSubmitTimeout):
					continue
				default:

					return
				}
			}
		}
	}
}

func (pool *WorkerPool[T]) submit(job *T) error {
	for {
		select {
		case <-pool.ctx().Done():
			return pool.ctx().Err()
		case <-time.After(pool.cfg.SubmitTimeout):
			return ErrSubmitTimeout
		case worker, ok := <-pool.workers.Claims():
			if !ok {
				return ErrDispatcherClosed
			}

			select {
			case <-pool.ctx().Done():
				return pool.ctx().Err()
			case <-time.After(pool.cfg.SubmitTimeout):
				return ErrSubmitTimeout
			case worker.input <- job:
				return nil
			}
		}
	}
}
