package workerpool

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

type (
	WorkerPool[T any] struct {
		workers []*Worker[T]
		wg      sync.WaitGroup
		jobs    *jobChannel[T]
		cfg     *Config[T]
	}

	Config[T any] struct {
		Jobs           *JobChannelConfig
		RateLimit      *rate.Limiter
		Events         Events[T]
		Handler        HandlerFunc[T]
		OnFailedSubmit func(error)
	}
)

func New[T any](cfg *Config[T]) (*WorkerPool[T], error) {
	if cfg == nil {
		return nil, ErrNilCfg
	}

	poolSize := cfg.Jobs.MaxSize
	if poolSize <= 0 {
		poolSize = defaultMaxSize
	}

	if cfg.OnFailedSubmit == nil {
		cfg.OnFailedSubmit = func(err error) {}
	}

	return &WorkerPool[T]{
		workers: make([]*Worker[T], 0, poolSize),
		jobs:    newJobChannel[T](cfg.Jobs),
		cfg:     cfg,
	}, nil
}

func (pool *WorkerPool[T]) Submit(ctx context.Context, job *T) error {
	if ctx == nil {
		return nil
	}

	if job == nil {
		return ErrNilJob
	}

	if err := pool.cfg.RateLimit.Wait(ctx); err != nil {
		return err
	}

	go func() {
		if err := pool.jobs.submit(ctx, job); err != nil {
			pool.cfg.OnFailedSubmit(err)
		}
	}()

	return nil
}
