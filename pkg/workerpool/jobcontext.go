package workerpool

import (
	"context"
	"sync"
	"time"
)

type (
	// JobAware is the argument passed to every HandlerFunc. It embeds
	// context.Context so handlers can honor cancellation and deadlines
	// with the usual ctx.Done() / ctx.Err() idioms, and exposes the job
	// payload via Job(). The underlying context is a composition of the
	// pool context (from New) and the caller context (from Submit):
	// whichever cancels first cancels the handler.
	JobAware[T any] interface {
		context.Context
		// Job returns the payload originally passed to Submit. The
		// value is stored by copy; mutating the returned value does
		// not affect other observers.
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

// newJobContext composes poolCtx and submitCtx into a single JobAware
// whose Done channel closes as soon as either parent cancels. It uses
// context.AfterFunc to avoid spawning a watcher goroutine per job;
// the two stop functions are captured under a mutex so each callback
// can cancel its sibling deterministically on first fire.
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

	var (
		stopMu               sync.Mutex
		stopPool, stopSubmit func() bool
	)

	// Hold stopMu across both AfterFunc registrations, so either callback
	// waiting to read the other's stop func sees both after initialization.
	stopMu.Lock()
	defer stopMu.Unlock()

	stopPool = context.AfterFunc(poolCtx, func() {
		ctx.once.Do(func() {
			ctx.err = poolCtx.Err()
			close(ctx.done)
		})
		stopMu.Lock()
		s := stopSubmit
		stopMu.Unlock()
		if s != nil {
			s()
		}
	})

	stopSubmit = context.AfterFunc(submitCtx, func() {
		ctx.once.Do(func() {
			ctx.err = submitCtx.Err()
			close(ctx.done)
		})
		stopMu.Lock()
		s := stopPool
		stopMu.Unlock()
		if s != nil {
			s()
		}
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

// Value looks up key in both parent contexts. When both parents hold a
// value for the same key, the submit-side value wins — callers who set
// request-scoped values on the Submit context can rely on them shadowing
// pool-wide defaults. When only one parent carries the key, that value
// is returned; when neither does, Value returns nil.
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
