package workerpool

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		id              uint64
		channelName     string
		running         atomic.Bool
		startedNotified atomic.Bool
		events          Events[T]
		done            chan struct{}
		input           chan *T
		handlerFn       func(context.Context, *T) error
		ctxFn           func() context.Context
		cancel          func()
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

// NewWorker constructs a Worker. The worker is not bound to any context until
// Join is called; a fresh per-Join context is created inside Join, which makes
// workers reusable across Join -> Leave -> Join cycles.
func NewWorker[T any](cfg *WorkerConfig[T]) (*Worker[T], error) {
	if cfg == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil worker config"))
	}

	if cfg.HandlerFunc == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil handler"))
	}

	w := &Worker[T]{
		id:        cfg.ID,
		handlerFn: cfg.HandlerFunc,
		events:    cfg.Events,
	}

	if w.events == nil {
		w.events = &NoopEvents[T]{}
	}

	return w, nil
}

func (w *Worker[T]) ID() uint64 {
	return w.id
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

// Join subscribes the worker to the given Jobs source using ctx as the parent
// for this Join cycle. Each Join builds a fresh cancellable context via
// newWorkerContext, so a worker can be Joined, Left, and Joined again.
func (w *Worker[T]) Join(ctx context.Context, jobs Jobs[T]) error {
	if ctx == nil {
		return ErrNilCtx
	}

	if err := ctx.Err(); err != nil {
		return err
	}

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
	w.ctxFn, w.cancel = newWorkerContext(ctx, w.id)
	w.input = make(chan *T)
	w.done = make(chan struct{})
	w.startedNotified.Store(false)
	w.channelName = jobs.Name()

	go w.runLoop(jobs.Claims())

	return nil
}

// Context returns the worker's per-Join context and true if the worker is
// currently Joined. It returns (nil, false) before the first Join and between
// a Leave and the next Join. Using the comma-ok form makes the "no active
// Join" state explicit and prevents callers from accidentally passing a nil
// context to downstream APIs.
func (w *Worker[T]) Context() (context.Context, bool) {
	if w.ctxFn == nil {
		return nil, false
	}

	return w.ctxFn(), true
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
			if !w.startedNotified.Swap(true) {
				w.events.WorkerStarted(w.ID())
			}
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
		// runLoop has exited (it deferred close(w.done)), so no goroutine
		// is reading w.ctxFn anymore. Clearing it here honors the contract
		// documented on Context(): "returns (nil, false) between a Leave
		// and the next Join". On the timeout branch we intentionally leave
		// these set — runLoop may still be executing and reading ctxFn.
		w.ctxFn = nil
		w.cancel = nil
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
