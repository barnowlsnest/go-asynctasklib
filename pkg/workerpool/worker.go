package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// CtxWorkerID is the context key under which each worker's ID is attached
// to the context passed to HandlerFunc. Handlers can retrieve it with
// ctx.Value(CtxWorkerID).(uint64).
const CtxWorkerID CtxString = "worker"

const defaultIdleTimeout = 100 * time.Millisecond

var (
	// ErrWorkerTimeout is returned from Worker.Leave when the worker did
	// not exit its runLoop within the supplied timeout.
	ErrWorkerTimeout = errors.New("worker timeout")
	// ErrWorkerPanic wraps a recovered panic from HandlerFunc and is
	// delivered to Events.JobFailed instead of propagating.
	ErrWorkerPanic = errors.New("worker panic")
	// ErrInvalidWorker is returned by constructors and Claims.Subscribe
	// when the worker or its config is nil or otherwise unusable.
	ErrInvalidWorker = errors.New("invalid worker")
	// ErrWorkerAlreadyRunning is returned from Worker.Join when the
	// worker is already subscribed to a Jobs source.
	ErrWorkerAlreadyRunning = errors.New("worker already subscribed")
)

type (
	// CtxString is the typed key used to attach values such as the worker
	// ID to a context without colliding with string keys from other
	// packages.
	CtxString string

	// Jobs is the interface a worker Join target must implement. Claims
	// is the canonical implementation used by WorkerPool.
	Jobs[T any] interface {
		Subscribe(WorkerCloser[T]) error
		Unsubscribe(WorkerCloser[T]) error
		Claims() chan *Claim[T]
		Name() string
	}

	// Worker is a single job-processing goroutine that subscribes to a
	// Jobs source and invokes HandlerFunc for every job it receives.
	// Workers are reusable across Join/Leave/Join cycles.
	Worker[T any] struct {
		channelName     string
		id              uint64
		idleTimeout     time.Duration
		mu              sync.Mutex
		lastActiveAt    atomic.Int64
		running         atomic.Bool
		startedNotified atomic.Bool
		events          Events[T]
		done            chan struct{}
		input           chan *T
		handlerFn       func(context.Context, *T) error
		ctxFn           func() context.Context
		cancel          func()
	}

	// Events is the observer interface for worker and job lifecycle
	// transitions. All methods are invoked synchronously from the worker
	// goroutine, so implementations should be fast and non-blocking;
	// offload any I/O to a separate goroutine. Pass NoopEvents as a base
	// and override only the hooks you care about.
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

	// WorkerConfig is the construction argument for NewWorker.
	WorkerConfig[T any] struct {
		// ID is the worker's stable numeric identity. It must be unique
		// within the owning Claims dispatcher.
		ID uint64
		// IdleTimeout is the max duration the worker will park on its
		// advertised claim before looping to re-advertise. Defaults to
		// 100ms.
		IdleTimeout time.Duration
		// Events is the lifecycle observer. Defaults to NoopEvents.
		Events Events[T]
		// HandlerFunc is the per-job callback. Required.
		HandlerFunc HandlerFunc[T]
	}

	// WorkerStatus is a point-in-time snapshot of a worker returned by
	// Worker.Status. It is safe to copy and inspect without a lock.
	WorkerStatus struct {
		Channel    string
		ID         uint64
		Running    bool
		Subscribed bool
	}

	// HandlerFunc is the signature of a per-job handler. It receives
	// the worker's per-Join context and a pointer to the job payload.
	// A non-nil return value is reported via Events.JobFailed; panics
	// are recovered, wrapped with ErrWorkerPanic, and reported the same
	// way without taking the worker down.
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
		id:          cfg.ID,
		handlerFn:   cfg.HandlerFunc,
		events:      cfg.Events,
		idleTimeout: cfg.IdleTimeout,
	}
	w.lastActiveAt.Store(time.Now().UTC().UnixNano())

	if w.events == nil {
		w.events = &NoopEvents[T]{}
	}

	if w.idleTimeout == time.Duration(0) {
		w.idleTimeout = defaultIdleTimeout
	}

	return w, nil
}

// ID returns the worker's stable numeric identity.
func (w *Worker[T]) ID() uint64 {
	return w.id
}

// LastActiveAt returns the Unix-nanosecond timestamp at which the worker
// last finished processing a job or was most recently Joined. The
// autoscaler reads it to decide when to Leave an idle worker.
func (w *Worker[T]) LastActiveAt() int64 {
	return w.lastActiveAt.Load()
}

// IsRunning reports whether the worker is currently processing a job. Used by
// the pool's autoscaler to avoid leaving a busy worker when scaling down.
func (w *Worker[T]) IsRunning() bool {
	return w.running.Load()
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

	// Reset the idle clock on every Join so autoscalers don't immediately
	// classify a freshly-joined worker as idle based on a stale timestamp.
	w.lastActiveAt.Store(time.Now().UnixNano())

	w.mu.Lock()
	w.ctxFn, w.cancel = newWorkerContext(ctx, w.id)
	w.input = make(chan *T)
	w.done = make(chan struct{})
	w.channelName = jobs.Name()
	w.mu.Unlock()

	w.startedNotified.Store(false)
	go w.runLoop(jobs.Claims())

	return nil
}

// Context returns the worker's per-Join context and true if the worker is
// currently Joined. It returns (nil, false) before the first Join and between
// a Leave and the next Join. Using the comma-ok form makes the "no active
// Join" state explicit and prevents callers from accidentally passing a nil
// context to downstream APIs.
func (w *Worker[T]) Context() (context.Context, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ctxFn == nil {
		return nil, false
	}

	return w.ctxFn(), true
}

func (w *Worker[T]) runLoop(jobs chan *Claim[T]) {
	defer close(w.done)
	for {
		select {
		case <-time.After(w.idleTimeout):
			continue
		case <-w.ctxFn().Done():
			w.running.Swap(false)
			return
		case jobs <- &Claim[T]{id: w.ID(), input: w.input}:
			if !w.startedNotified.Swap(true) {
				w.events.WorkerStarted(w.ID())
			}

			select {
			case <-w.ctxFn().Done():
				w.running.Swap(false)
				return
			case job := <-w.input:
				w.running.Store(true)
				if err := w.processJob(job); err != nil {
					w.events.JobFailed(err, job)
				} else {
					w.events.JobOk(job)
				}

				w.running.Store(false)
				w.lastActiveAt.Store(time.Now().UnixNano())
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

	if err := w.ctxFn().Err(); err != nil {
		return err
	}

	return w.handlerFn(w.ctxFn(), job)
}

// Leave unsubscribes the worker from jobs and cancels its per-Join
// context, then waits up to timeout for the run loop to exit. It
// returns ErrWorkerTimeout if the worker does not stop within timeout.
// Leave is safe to call from a different goroutine than Join.
func (w *Worker[T]) Leave(jobs Jobs[T], timeout time.Duration) error {
	if jobs == nil {
		return fmt.Errorf("%w: nil jobs", ErrNil)
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
		w.ctxFn = nil
		w.cancel = nil
		return nil
	case <-time.After(timeout):
		w.events.LeaveTimeout(w.ID(), timeout)
		return ErrWorkerTimeout
	}
}

// Status returns a point-in-time snapshot of the worker's identity,
// channel, and running state. The returned struct is safe to retain.
func (w *Worker[T]) Status() *WorkerStatus {
	return &WorkerStatus{
		ID:      w.ID(),
		Running: w.running.Load(),
		Channel: w.channelName,
	}
}

// String implements fmt.Stringer, producing a compact debug format that
// distinguishes running workers ("+worker:N->[channel]") from idle or
// detached ones ("-worker:N").
func (w *Worker[T]) String() string {
	if w.running.Load() {
		return fmt.Sprintf("+worker:%d->[%s]", w.ID(), w.channelName)
	}

	return fmt.Sprintf("-worker:%d", w.ID())
}
