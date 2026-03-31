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
		workers       map[uint64]*Worker[T]
		submitter     Submitter[T]
		rateLimit     *rate.Limiter
		parentContext ParentContextFunc
		workerFactory WorkerFactory[T]
	}

	Submitter[T any] interface {
		Submit(ctx context.Context, job *T) (*Submission, error)
		MaxSize() int
	}
)

func New[T any](
	parentCtx context.Context, rateLimit *rate.Limiter, submitter Submitter[T], workerFactory WorkerFactory[T],
) (*WorkerPool[T], error) {
	switch {
	case parentCtx == nil:
		return nil, ErrNilCtx
	case parentCtx.Err() != nil:
		return nil, parentCtx.Err()
	case submitter == nil:
		return nil, errors.Join(ErrCfg, errors.New("nil job channel"))
	case workerFactory == nil:
		return nil, errors.Join(ErrCfg, errors.New("nil worker factory"))
	}

	if rateLimit == nil {
		rateLimit = rate.NewLimiter(rate.Inf, 0)
	}

	p := WorkerPool[T]{
		rateLimit: rateLimit,
		submitter: submitter,
		workers:   make(map[uint64]*Worker[T], submitter.MaxSize()),
		parentContext: func() context.Context {
			return parentCtx
		},
	}

	for i := range submitter.MaxSize() {
		w, err := p.newWorker(uint64(i + 1))
		if err != nil {
			return nil, err
		}

		p.addWorker(w)
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

	return wp.submitter.Submit(ctx, job)
}

func (wp *WorkerPool[T]) newWorker(id uint64) (*Worker[T], error) {
	w, err := wp.workerFactory(wp.parentContext(), id)
	if err != nil {
		return nil, err
	}

	return w, nil
}

func (wp *WorkerPool[T]) addWorker(w *Worker[T]) {
	if w == nil {
		return
	}

	wp.workers[w.ID()] = w
}
