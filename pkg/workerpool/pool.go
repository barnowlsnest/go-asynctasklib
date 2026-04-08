package workerpool

import (
	"context"
	"errors"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidPool = errors.New("invalid pool configuration")

type (
	WorkerPool[T any] struct {
		done      chan struct{}
		backlog   chan *T
		dlq       chan *T
		dlqBackup []*T
		ctx       func() context.Context
		cancel    context.CancelFunc
		limiter   *rate.Limiter
		cfg       *Config
		workers   *Claims[T]
	}

	Config struct {
		RateLimit            float64
		SubmitBackoff        time.Duration
		SubmitTimeout        time.Duration
		SubmitAttemptsPerSec int
		MaxSubmitRetries     int
		Backlog              int
		Size                 int
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
		workers:   workers,
		cfg:       cfg,
		backlog:   make(chan *T, cfg.Backlog),
		dlq:       make(chan *T, cfg.Backlog),
		dlqBackup: make([]*T, 0, cfg.Backlog),
		ctx:       func() context.Context { return poolCtx },
		cancel:    cancel,
		limiter:   rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.Size),
	}

	go pool.start()

	return pool, nil
}

func (pool *WorkerPool[T]) Submit(job *T) error {
	if job == nil {
		return ErrNilJob
	}

	if err := pool.limiter.Wait(pool.ctx()); err != nil {
		return err
	}

	select {
	case pool.backlog <- job:
		return nil
	case <-time.After(pool.cfg.SubmitTimeout):
		return ErrSubmitTimeout
	}
}

func (pool *WorkerPool[T]) start() {
	for {
		select {
		case <-pool.ctx().Done():
			return
		case job, ok := <-pool.backlog:
			if !ok {
				return
			}

			err := pool.submit(job)
			switch {
			case errors.Is(err, ErrSubmitTimeout):
				select {
				case <-pool.ctx().Done():
					return
				case pool.dlq <- job:
					continue
				case <-time.After(pool.cfg.SubmitTimeout):
					pool.dlqBackup = append(pool.dlqBackup, job)
					continue
				}
			default:
				return
			}
		case <-time.After(time.Second):
			continue
		}
	}
}

func (pool *WorkerPool[T]) submit(job *T) error {
	for {
		select {
		case <-pool.ctx().Done():
			return pool.ctx().Err()
		case worker, ok := <-pool.workers.Claims():
			if !ok {
				return ErrDispatcherClosed
			}
			select {
			case <-pool.ctx().Done():
				return pool.ctx().Err()
			case worker.input <- job:
				return nil
			case <-time.After(pool.cfg.SubmitTimeout):
				return ErrSubmitTimeout
			}
		}
	}
}
