package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const defaultIdleTimeout = 100 * time.Millisecond

var (
	// ErrWorkerTimeout is returned from worker.Leave when the worker did
	// not exit its runLoop within the supplied timeout.
	ErrWorkerTimeout = errors.New("worker timeout")
	// ErrWorkerPanic wraps a recovered panic from HandlerFunc and is
	// delivered to Events.JobFailed instead of propagating.
	ErrWorkerPanic = errors.New("worker panic")
	// ErrInvalidWorker is returned by constructors and claims.Subscribe
	// when the worker or its config is nil or otherwise unusable.
	ErrInvalidWorker = errors.New("invalid worker")
	// ErrWorkerAlreadyRunning is returned from worker.Join when the
	// worker is already subscribed to a jobs source.
	ErrWorkerAlreadyRunning = errors.New("worker already subscribed")
)

type (
	// Jobs is the interface a worker Join target must implement. claims
	// is the canonical implementation used by WorkerPool.
	Jobs[T any] interface {
		Subscribe(Subscriber[T]) error
		Unsubscribe(Subscriber[T]) error
		Claims() chan *claim[T]
		Name() string
	}

	// Events is the observer interface for worker and job lifecycle
	// transitions. All methods are invoked synchronously from the worker
	// goroutine, so implementations should be fast and non-blocking;
	// offload any I/O to a separate goroutine. Pass NoopEvents as a base
	// and override only the hooks you care about.
	Events[T any] interface {
		WorkerStarted(uint64)
		WorkerStopped(uint64)
		JobFailed(error, T)
		JobOk(T)
		Subscribed(uint64)
		SubscribeFailed(error, uint64)
		Unsubscribed(uint64)
		UnsubscribeFailed(error, uint64)
		LeaveTimeout(uint64, time.Duration)
	}

	worker[T any] struct {
		channelName     string
		id              uint64
		idleTimeout     time.Duration
		mu              sync.Mutex
		lastActiveAt    atomic.Int64
		running         atomic.Bool
		startedNotified atomic.Bool
		events          Events[T]
		done            chan struct{}
		input           chan JobAware[T]
		handlerFn       HandlerFunc[T]
		ctxFn           func() context.Context
		cancel          func()
	}

	workerConfig[T any] struct {
		// ID is the worker's stable numeric identity. It must be unique
		// within the owning claims dispatcher.
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

	// HandlerFunc is the signature of a per-job handler. It receives
	// the worker's per-Join context and a pointer to the job payload.
	// A non-nil return value is reported via Events.JobFailed; panics
	// are recovered, wrapped with ErrWorkerPanic, and reported the same
	// way without taking the worker down.
	HandlerFunc[T any] func(JobAware[T]) error
)

func newWorkerContext(parentCtx context.Context) (ctxFunc func() context.Context, cancelFunc func()) {
	var ctxWithCancel context.Context
	ctxWithCancel, cancelFunc = context.WithCancel(parentCtx)
	ctxFunc = func() context.Context {
		return ctxWithCancel
	}

	return ctxFunc, cancelFunc
}

func newWorker[T any](cfg *workerConfig[T]) (*worker[T], error) {
	if cfg == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil worker config"))
	}

	if cfg.HandlerFunc == nil {
		return nil, errors.Join(ErrInvalidWorker, errors.New("nil handler"))
	}

	w := &worker[T]{
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

func (w *worker[T]) ID() uint64 {
	return w.id
}

// LastActiveAt returns the Unix-nanosecond timestamp at which the worker
// last finished processing a job or was most recently Joined. The
// autoscaler reads it to decide when to leave an idle worker.
func (w *worker[T]) LastActiveAt() int64 {
	return w.lastActiveAt.Load()
}

// IsRunning reports whether the worker is currently processing a job. Used by
// the pool's autoscaler to avoid leaving a busy worker when scaling down.
func (w *worker[T]) IsRunning() bool {
	return w.running.Load()
}

func (w *worker[T]) join(ctx context.Context, jobs Jobs[T]) error {
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
	w.lastActiveAt.Store(time.Now().UnixNano())

	w.mu.Lock()
	w.ctxFn, w.cancel = newWorkerContext(ctx)
	w.input = make(chan JobAware[T])
	w.done = make(chan struct{})
	w.channelName = jobs.Name()
	w.mu.Unlock()

	w.startedNotified.Store(false)
	go w.runLoop(jobs.Claims())

	return nil
}

// Context returns the worker's per-Join context and true if the worker is
// currently Joined. It returns (nil, false) before the first join and between
// a leave and the next join. Using the comma-ok form makes the "no active
// join" state explicit and prevents callers from accidentally passing a nil
// context to downstream APIs.
func (w *worker[T]) Context() (context.Context, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ctxFn == nil {
		return nil, false
	}

	return w.ctxFn(), true
}

func (w *worker[T]) runLoop(jobs chan *claim[T]) {
	defer close(w.done)
	for {
		select {
		case <-time.After(w.idleTimeout):
			continue
		case <-w.ctxFn().Done():
			w.running.Swap(false)
			return
		case jobs <- &claim[T]{id: w.ID(), input: w.input}:
			if !w.startedNotified.Swap(true) {
				w.events.WorkerStarted(w.ID())
			}

			select {
			case <-w.ctxFn().Done():
				w.running.Swap(false)
				return
			case ctx := <-w.input:
				w.running.Store(true)
				if err := w.processJob(ctx); err != nil {
					w.events.JobFailed(err, ctx.Job())
				} else {
					w.events.JobOk(ctx.Job())
				}

				w.running.Store(false)
				w.lastActiveAt.Store(time.Now().UnixNano())
			}
		}
	}
}

func (w *worker[T]) processJob(ctx JobAware[T]) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(ErrWorkerPanic, fmt.Errorf("%+v", r))
		}
	}()

	return w.handlerFn(ctx)
}

func (w *worker[T]) leave(jobs Jobs[T], timeout time.Duration) error {
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
		w.mu.Lock()
		w.ctxFn = nil
		w.cancel = nil
		w.mu.Unlock()
		return nil
	case <-time.After(timeout):
		w.events.LeaveTimeout(w.ID(), timeout)
		return ErrWorkerTimeout
	}
}
