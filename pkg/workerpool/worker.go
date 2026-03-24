package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const CtxWorkerID CtxString = "worker"

type (
	CtxString string

	Worker[T any] struct {
		events    Events[T]
		jobs      *jobChannel[T]
		onceJoin  sync.Once
		onceLeave sync.Once
		done      chan struct{}
		jobInput  chan *T
		handlerFn func(context.Context, *T) error
		ctxFn     func() context.Context
		cancel    func()
		started   atomic.Bool
	}

	Events[T any] interface {
		WorkerStarted(uint64)
		WorkerStopped(uint64)
		JobFailed(error, *T)
		JobOk(*T)
		Subscribed(uint64)
		SubscribeFailed(error, uint64)
		Unsubscribed(uint64)
	}

	WorkerConfig[T any] struct {
		ID uint64
		Events[T]
		HandlerFunc[T]
	}

	claim[T any] struct {
		id    uint64
		jobCh chan *T
	}

	HandlerFunc[T any] func(context.Context, *T) error

	dispatcher[T any] chan claim[T]
)

func newWorkerContext(parentCtx context.Context, id uint64) (ctxFunc func() context.Context, cancelFunc func()) {
	var ctxWithCancel, ctxWithVal context.Context
	ctxWithVal = context.WithValue(parentCtx, CtxWorkerID, id)
	ctxWithCancel, cancelFunc = context.WithCancel(ctxWithVal)
	ctxFunc = func() context.Context {
		return ctxWithCancel
	}

	return ctxFunc, cancelFunc
}

func NewWorker[T any](parentCtx context.Context, cfg *WorkerConfig[T]) (*Worker[T], error) {
	pCtx := parentCtx
	if pCtx == nil {
		pCtx = context.Background()
	}

	if errCtx := pCtx.Err(); errCtx != nil {
		return nil, errCtx
	}

	if cfg == nil {
		return nil, ErrInvalidWorker
	}

	w := &Worker[T]{
		done:      make(chan struct{}),
		handlerFn: cfg.HandlerFunc,
		events:    cfg.Events,
	}

	if w.handlerFn == nil {
		return nil, ErrInvalidWorker
	}
	if w.events == nil {
		w.events = &NoopEvents[T]{}
	}

	w.ctxFn, w.cancel = newWorkerContext(parentCtx, cfg.ID)

	return w, nil
}

func (w *Worker[T]) ID() uint64 {
	return w.ctxFn().Value(CtxWorkerID).(uint64)
}

func (w *Worker[T]) Join(jobs *jobChannel[T]) {
	if jobs == nil {
		return
	}

	w.onceJoin.Do(func() {
		if err := jobs.subscribe(w); err != nil {
			w.events.SubscribeFailed(err, w.ID())
			return
		}

		w.events.Subscribed(w.ID())
		w.jobs = jobs // for the reference when unsubscribing
		w.jobInput = make(chan *T)
		w.runLoop(jobs.jobs())
	})
}

func (w *Worker[T]) runLoop(dispatcher dispatcher[T]) {
	defer close(w.done)
	var onceStarted sync.Once
	for {
		select {
		case <-w.ctxFn().Done():
			return
		case dispatcher <- claim[T]{id: w.ID(), jobCh: w.jobInput}:
			onceStarted.Do(func() {
				w.started.Store(true)
				w.events.WorkerStarted(w.ID())
			})
			continue
		case job, ok := <-w.jobInput:
			if !ok {
				dispatcher = nil
				continue
			}
			if err := w.processJob(job); err != nil {
				w.events.JobFailed(err, job)
			} else {
				w.events.JobOk(job)
			}
		}
	}
}

func (w *Worker[T]) processJob(job *T) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(ErrWorkerPanic, fmt.Errorf("%+v", r))
		}
	}()

	if err = w.ctxFn().Err(); err != nil {
		return err
	}

	return w.handlerFn(w.ctxFn(), job)
}

func (w *Worker[T]) LeaveJobChannel() {
	if w.jobs == nil {
		return
	}

	w.onceLeave.Do(func() {
		w.jobs.unsubscribe(w)
		w.events.Unsubscribed(w.ID())
		close(w.jobInput)
		w.jobInput = nil
	})
}

func (w *Worker[T]) Shutdown(timeout time.Duration) error {
	if !w.started.Load() {
		return nil
	}

	defer w.events.WorkerStopped(w.ID())
	w.LeaveJobChannel()
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-time.After(timeout):
		return ErrWorkerTimeout
	}
}
