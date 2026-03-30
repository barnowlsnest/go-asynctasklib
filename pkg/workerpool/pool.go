package workerpool

import (
	"context"
	"errors"

	"golang.org/x/time/rate"
)

type (
	ParentContextFunc func() context.Context

	WorkerFactory[T any] func(ctx context.Context, id uint64) (*Worker[T], error)

	WorkerPool[T any] struct {
		ids           uint64
		workers       map[uint64]*Worker[T]
		jobs          *Channel[T]
		rateLimit     *rate.Limiter
		parentContext ParentContextFunc
		workerFactory WorkerFactory[T]
	}
)

func New[T any](
	parentCtx context.Context, rateLimit *rate.Limiter, jobs *Channel[T], workerFactory WorkerFactory[T],
) (*WorkerPool[T], error) {
	switch {
	case parentCtx == nil:
		return nil, ErrNilCtx
	case parentCtx.Err() != nil:
		return nil, parentCtx.Err()
	case jobs == nil:
		return nil, errors.Join(ErrCfg, errors.New("nil job channel"))
	case workerFactory == nil:
		return nil, errors.Join(ErrCfg, errors.New("nil worker factory"))
	}

	if rateLimit == nil {
		rateLimit = rate.NewLimiter(rate.Inf, 0)
	}

	p := WorkerPool[T]{
		rateLimit: rateLimit,
		jobs:      jobs,
		workers:   make(map[uint64]*Worker[T], jobs.MaxSize()),
		parentContext: func() context.Context {
			return parentCtx
		},
	}

	if err := p.addWorker(); err != nil {
		return nil, err
	}

	return &p, nil
}

func (wp *WorkerPool[T]) Submit(ctx context.Context, job *T) (*Submission, error) {
	switch {
	case ctx == nil:
		return nil, ErrNilCtx
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case job == nil:
		return nil, ErrNilJob
	}

	if err := wp.rateLimit.Wait(ctx); err != nil {
		return nil, err
	}

	_ = wp.scale(1)

	return wp.jobs.Submit(ctx, job), nil
}

func (wp *WorkerPool[T]) scale(n int) error {
	switch {
	case n > 0:
		return wp.up(n)
	case n < 0:

		return wp.down(n)
	default:
		return nil
	}
}

func (wp *WorkerPool[T]) addWorker() error {
	cpID := wp.ids
	nID := cpID + 1
	w, err := wp.workerFactory(wp.parentContext(), nID)
	if err != nil {
		return err
	}

	wp.ids = w.ID()
	wp.workers[w.ID()] = w

	return nil
}

func (wp *WorkerPool[T]) up(n int) error {
	return nil
}

func (wp *WorkerPool[T]) down(n int) error {
	return nil
}
