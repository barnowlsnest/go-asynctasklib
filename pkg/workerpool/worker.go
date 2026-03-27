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

	Workable[T any] interface {
		Subscribe(*Worker[T]) error
		Unsubscribe(*Worker[T])
		Claims() WorkClaims[T]
	}

	Worker[T any] struct {
		mu          sync.Mutex
		onceStarted sync.Once
		events      Events[T]
		jobs        Workable[T]
		done        chan struct{}
		jobInput    chan *T
		handlerFn   func(context.Context, *T) error
		ctxFn       func() context.Context
		cancel      func()
		started     atomic.Bool
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

	WorkClaim[T any] struct {
		id    uint64
		jobCh chan *T
	}

	HandlerFunc[T any] func(context.Context, *T) error

	WorkClaims[T any] chan WorkClaim[T]
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

	w.ctxFn, w.cancel = newWorkerContext(pCtx, cfg.ID)

	return w, nil
}

func (w *Worker[T]) ID() uint64 {
	return w.ctxFn().Value(CtxWorkerID).(uint64)
}

func (w *Worker[T]) Join(jobs Workable[T]) error {
	if jobs == nil {
		return ErrNilJob
	}

	if w.started.Load() {
		return ErrWorkerAlreadyJoined
	}

	if err := jobs.Subscribe(w); err != nil {
		w.events.SubscribeFailed(err, w.ID())
		return err
	}

	w.mu.Lock()
	w.jobs = jobs
	w.mu.Unlock()

	w.events.Subscribed(w.ID())
	w.jobInput = make(chan *T)
	w.runLoop(jobs.Claims())

	return nil
}

func (w *Worker[T]) runLoop(ch WorkClaims[T]) {
	defer close(w.done)
	for {
		select {
		case <-w.ctxFn().Done():
			return
		case ch <- WorkClaim[T]{id: w.ID(), jobCh: w.jobInput}:
			w.onceStarted.Do(func() {
				w.started.Store(true)
				w.events.WorkerStarted(w.ID())
			})
			continue
		case job, ok := <-w.jobInput:
			if !ok {
				ch = nil
				w.jobInput = nil
				continue
			}

			if err := w.processJob(job); err != nil {
				w.events.JobFailed(err, job)
				continue
			}

			w.events.JobOk(job)
		}
	}
}

func (w *Worker[T]) processJob(job *T) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(ErrWorkerPanic, fmt.Errorf("%+v", r))
		}
	}()

	if err := w.ctxFn().Err(); err != nil {
		return err
	}

	return w.handlerFn(w.ctxFn(), job)
}

func (w *Worker[T]) Leave() {
	defer func() {
		w.events.Unsubscribed(w.ID())
	}()
	w.leave()
	close(w.jobInput)
}

func (w *Worker[T]) leave() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.jobs == nil {
		return
	}

	w.jobs.Unsubscribe(w)
}

func (w *Worker[T]) Shutdown(timeout time.Duration) error {
	if !w.started.Load() {
		return nil
	}

	defer w.events.WorkerStopped(w.ID())
	w.Leave()
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-time.After(timeout):
		return ErrWorkerTimeout
	}
}
