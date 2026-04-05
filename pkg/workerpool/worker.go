package workerpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const CtxWorkerID CtxString = "worker"

const idleTimeout = 100 * time.Millisecond

var (
	ErrWorkerTimeout        = errors.New("worker timeout")
	ErrWorkerPanic          = errors.New("worker panic")
	ErrInvalidWorker        = errors.New("invalid worker")
	ErrWorkerAlreadyRunning = errors.New("worker already subscribed")
)

type (
	CtxString string

	Jobs[T any] interface {
		Subscribe(WorkerCloser[T]) error
		Unsubscribe(WorkerCloser[T]) error
		Claims() chan *Claim[T]
		Name() string
	}

	Worker[T any] struct {
		io.Closer
		channelName string
		running     atomic.Bool
		onceStarted sync.Once
		events      Events[T]
		done        chan struct{}
		input       chan *T
		handlerFn   func(context.Context, *T) error
		ctxFn       func() context.Context
		cancel      func()
	}

	Events[T any] interface {
		WorkerStarted(uint64)
		WorkerStopped(uint64)
		JobFailed(error, *T)
		JobOk(*T)
		Subscribed(uint64)
		SubscribeFailed(error, uint64)
		Unsubscribed(uint64)
		UnsubscribeFailed(error, uint64)
		LeaveTimeout(uint64, time.Duration)
	}

	WorkerConfig[T any] struct {
		ID          uint64
		IdleTimeout time.Duration
		Events      Events[T]
		HandlerFunc HandlerFunc[T]
	}

	WorkerStatus struct {
		Channel    string
		ID         uint64
		Running    bool
		Subscribed bool
	}

	HandlerFunc[T any] func(context.Context, *T) error
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
		return nil, ErrNilCtx
	}

	if errCtx := pCtx.Err(); errCtx != nil {
		return nil, errCtx
	}

	if cfg == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil worker config"))
	}

	w := &Worker[T]{
		input:     make(chan *T),
		done:      make(chan struct{}),
		handlerFn: cfg.HandlerFunc,
		events:    cfg.Events,
	}

	if w.handlerFn == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil handler"))
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

func (w *Worker[T]) Close() error {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.Join(ErrWorkerPanic, errors.New("panic on closing job input chan"))
			}
		}()
		close(w.input)
	}()

	return err
}

func (w *Worker[T]) Join(jobs Jobs[T]) error {
	if jobs == nil {
		return ErrNilJob
	}

	if w.running.Load() {
		return ErrWorkerAlreadyRunning
	}

	if err := jobs.Subscribe(w); err != nil {
		w.events.SubscribeFailed(err, w.ID())
		return err
	}

	w.events.Subscribed(w.ID())
	w.input = make(chan *T)
	w.channelName = jobs.Name()

	go w.runLoop(jobs.Claims())

	return nil
}

func (w *Worker[T]) Context() context.Context {
	return w.ctxFn()
}

func (w *Worker[T]) runLoop(jobs chan *Claim[T]) {
	defer close(w.done)

	for {
		select {
		case <-time.After(idleTimeout):
			continue
		case <-w.ctxFn().Done():
			w.running.Swap(false)
			return
		case jobs <- &Claim[T]{id: w.ID(), input: w.input}:
			w.running.Swap(true)
			w.onceStarted.Do(func() {
				w.events.WorkerStarted(w.ID())
			})
			continue
		case job, ok := <-w.input:
			if !ok {
				w.input = nil
				jobs = nil
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

func (w *Worker[T]) Leave(jobs Jobs[T], timeout time.Duration) error {
	if jobs == nil {
		return nil
	}

	if err := jobs.Unsubscribe(w); err != nil {
		w.events.UnsubscribeFailed(err, w.ID())
		return err
	}

	w.events.Unsubscribed(w.ID())
	defer w.events.WorkerStopped(w.ID())
	w.cancel()

	select {
	case <-w.done:
		return nil
	case <-time.After(timeout):
		w.events.LeaveTimeout(w.ID(), timeout)
		return ErrWorkerTimeout
	}
}

func (w *Worker[T]) Status() *WorkerStatus {
	return &WorkerStatus{
		ID:      w.ID(),
		Running: w.running.Load(),
		Channel: w.channelName,
	}
}

func (w *Worker[T]) String() string {
	if w.running.Load() {
		return fmt.Sprintf("+worker:%d->[%s]", w.ID(), w.channelName)
	}

	return fmt.Sprintf("-worker:%d", w.ID())
}
