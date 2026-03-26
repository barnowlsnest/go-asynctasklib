package workerpool

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

type (
	WorkerPool[T any] struct {
		workers []*Worker[T]
		jobs    *Channel[T]
		cfg     *Config[T]
	}

	Config[T any] struct {
		Jobs           *ChannelConfig
		RateLimit      *rate.Limiter
		Events         Events[T]
		Handler        HandlerFunc[T]
		OnFailedSubmit func(error)
	}
)

func New[T any](parentCtx context.Context, cfg *Config[T]) (*WorkerPool[T], error) {
	switch {
	case parentCtx == nil:
		return nil, ErrNilCfg
	case parentCtx.Err() != nil:
		return nil, parentCtx.Err()
	case cfg == nil:
		return nil, ErrNilCfg
	case cfg.Jobs == nil:
		return nil, ErrNilCfg
	}

	poolSize := cfg.Jobs.MaxSize
	if poolSize <= 0 {
		poolSize = defaultMaxSize
	}

	if cfg.OnFailedSubmit == nil {
		cfg.OnFailedSubmit = func(err error) {}
	}

	if cfg.RateLimit == nil {
		cfg.RateLimit = rate.NewLimiter(rate.Inf, 0)
	}

	return &WorkerPool[T]{
		workers: make([]*Worker[T], 0, poolSize),
		jobs:    NewChannel[T](cfg.Jobs),
		cfg:     cfg,
	}, nil
}

func (wp *WorkerPool[T]) Submit(ctx context.Context, job *T) error {
	switch {
	case ctx == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case job == nil:
		return ErrNilJob
	}

	rl := wp.cfg.RateLimit
	if err := rl.Wait(ctx); err != nil {
		return err
	}

	errCh := make(chan error)
	go func() {
		defer close(errCh)
		s := wp.jobs.Submit(ctx, job)
		select {
		case <-s.Done():
			errCh <- s.Err()
		case <-time.After(time.Second):
			s.Cancel()
			errCh <- ErrSubmitTimeout
		}
	}()

	return <-errCh

}
