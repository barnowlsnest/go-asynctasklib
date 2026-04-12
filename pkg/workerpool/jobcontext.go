package workerpool

import (
	"context"
	"sync"
	"time"
)

type (
	JobAware[T any] interface {
		context.Context
		Job() T
	}

	jobContext[T any] struct {
		poolCtx   context.Context
		submitCtx context.Context
		job       T
		done      chan struct{}
		err       error
		once      sync.Once
	}
)

func newJobContext[T any](poolCtx, submitCtx context.Context, job T) *jobContext[T] {
	switch {
	case poolCtx == nil:
		panic("pool context cannot be nil")
	case submitCtx == nil:
		panic("submit context cannot be nil")
	}

	ctx := &jobContext[T]{
		poolCtx:   poolCtx,
		submitCtx: submitCtx,
		job:       job,
		done:      make(chan struct{}),
	}

	var stopPool, stopSubmit func() bool

	stopPool = context.AfterFunc(poolCtx, func() {
		ctx.once.Do(func() {
			ctx.err = poolCtx.Err()
			close(ctx.done)
		})
		stopSubmit()
	})

	stopSubmit = context.AfterFunc(submitCtx, func() {
		ctx.once.Do(func() {
			ctx.err = submitCtx.Err()
			close(ctx.done)
		})
		stopPool()
	})

	return ctx
}

func (ctx *jobContext[T]) Deadline() (time.Time, bool) {
	d1, ok1 := ctx.poolCtx.Deadline()
	d2, ok2 := ctx.submitCtx.Deadline()

	switch {
	case ok1 && ok2:
		if d1.Before(d2) {
			return d1, true
		}
		return d2, true
	case ok1:
		return d1, true
	case ok2:
		return d2, true
	default:
		return time.Time{}, false
	}
}

func (ctx *jobContext[T]) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *jobContext[T]) Err() error {
	select {
	case <-ctx.done:
		return ctx.err
	default:
		return nil
	}
}

func (ctx *jobContext[T]) Value(key any) any {
	poolVal, submitVal := ctx.poolCtx.Value(key), ctx.submitCtx.Value(key)
	if poolVal != submitVal {
		switch {
		case poolVal == nil && submitVal == nil:
			return nil
		case poolVal == nil:
			return submitVal
		case submitVal == nil:
			return poolVal
		default:
			return submitVal
		}
	}

	return submitVal
}

func (ctx *jobContext[T]) Job() T {
	return ctx.job
}
