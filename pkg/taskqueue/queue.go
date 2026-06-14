package taskqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/yielder"
)

const (
	defaultMaxAttempts       = 3
	defaultVisibilityTimeout = 30 * time.Second
	defaultReapInterval      = 5 * time.Second
	defaultDequeuePoll       = 5 * time.Millisecond
)

// Queue orchestrates a Backend and a DeadLetter, enforcing the max-attempts
// retry/DLQ policy and emitting QueueEvents. Construct with New; tear down
// with Close.
type Queue struct {
	backend      Backend
	deadLetter   DeadLetter
	events       QueueEvents
	maxAttempts  int
	mode         Mode
	visibility   time.Duration
	reapInterval time.Duration
	clock        func() time.Time

	cancel     context.CancelFunc
	reaperDone chan struct{}
	closeOnce  sync.Once
	closed     atomic.Bool
}

// Option configures a Queue during New.
type Option func(*Queue)

// WithBackend sets a custom Backend. When set, WithMode is ignored (ordering
// belongs to the backend). Defaults to an in-memory backend.
func WithBackend(backend Backend) Option {
	return func(q *Queue) { q.backend = backend }
}

// WithMode selects the ordering discipline for the default in-memory backend.
func WithMode(mode Mode) Option {
	return func(q *Queue) { q.mode = mode }
}

// WithDeadLetter sets a custom DeadLetter. Defaults to in-memory.
func WithDeadLetter(deadLetter DeadLetter) Option {
	return func(q *Queue) { q.deadLetter = deadLetter }
}

// WithMaxAttempts sets how many deliveries a task gets before it is
// dead-lettered. Requeue is immediate (no backoff).
func WithMaxAttempts(attempts int) Option {
	return func(q *Queue) { q.maxAttempts = attempts }
}

// WithVisibilityTimeout sets how long a claimed task stays invisible before
// its lease expires.
func WithVisibilityTimeout(timeout time.Duration) Option {
	return func(q *Queue) { q.visibility = timeout }
}

// WithReapInterval sets how often expired leases are swept.
func WithReapInterval(interval time.Duration) Option {
	return func(q *Queue) { q.reapInterval = interval }
}

// WithEvents sets the lifecycle observer.
func WithEvents(events QueueEvents) Option {
	return func(q *Queue) { q.events = events }
}

// New builds a Queue bound to ctx and starts its reaper goroutine. Canceling
// ctx or calling Close stops the reaper.
func New(ctx context.Context, opts ...Option) (*Queue, error) {
	if ctx == nil {
		return nil, errors.Join(ErrInvalidQueue, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	queue := &Queue{
		events:       NewNoopQueueEvents(),
		maxAttempts:  defaultMaxAttempts,
		mode:         ModePriority,
		visibility:   defaultVisibilityTimeout,
		reapInterval: defaultReapInterval,
		clock:        time.Now,
		reaperDone:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(queue)
	}

	if queue.backend == nil {
		queue.backend = NewMemoryBackend(queue.mode)
	}
	if queue.deadLetter == nil {
		queue.deadLetter = NewMemoryDeadLetter()
	}
	if queue.maxAttempts <= 0 {
		queue.maxAttempts = defaultMaxAttempts
	}
	if queue.visibility <= 0 {
		queue.visibility = defaultVisibilityTimeout
	}
	if queue.reapInterval <= 0 {
		queue.reapInterval = defaultReapInterval
	}

	runCtx, cancel := context.WithCancel(ctx)
	queue.cancel = cancel

	go func() {
		defer close(queue.reaperDone)
		queue.reaper(runCtx)
	}()

	return queue, nil
}

func (q *Queue) reaper(ctx context.Context) {
	ticker := time.NewTicker(q.reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.sweep(ctx)
		}
	}
}

// sweep routes every expired lease through the retry/DLQ policy.
func (q *Queue) sweep(ctx context.Context) {
	expired, err := q.backend.ReapExpired(ctx, q.clock())
	if err != nil {
		return
	}
	for _, lease := range expired {
		q.events.OnLeaseExpired(lease)
		_ = q.route(ctx, lease, ErrLeaseExpired)
	}
}

// route applies the retry/DLQ policy to a failed or expired lease. Below the
// attempt ceiling it requeues; at or above it, it dead-letters. This is the
// single enforcement point shared by Claim.Nack and the reaper sweep.
func (q *Queue) route(ctx context.Context, lease Lease, reason error) error {
	if lease.Attempt() >= q.maxAttempts {
		q.events.OnDeadLetter(lease.Task(), reason)
		if err := q.deadLetter.Add(ctx, lease.Task(), reason); err != nil {
			return err
		}
		return q.backend.Nack(ctx, lease.Token(), false)
	}
	q.events.OnRetry(lease.Task(), lease.Attempt())
	return q.backend.Nack(ctx, lease.Token(), true)
}

// Close cancels the reaper and waits for it to exit. Idempotent.
func (q *Queue) Close(ctx context.Context) error {
	var err error
	q.closeOnce.Do(func() {
		q.closed.Store(true)
		q.cancel()
		select {
		case <-q.reaperDone:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

// Enqueue stores task for ordered delivery.
func (q *Queue) Enqueue(ctx context.Context, task Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.closed.Load() {
		return ErrQueueClosed
	}
	if task == nil {
		return ErrTaskNil
	}
	if err := q.backend.Enqueue(ctx, task); err != nil {
		return err
	}
	q.events.OnEnqueue(task)
	return nil
}

// Claim is a queue-level handle for a leased task. The consumer must settle
// it exactly once with Ack (success) or Nack (failure).
type Claim struct {
	queue   *Queue
	lease   Lease
	settled atomic.Bool
}

// Task returns the leased task.
func (c *Claim) Task() Task { return c.lease.Task() }

// Ack marks the task done and removes it from the backend.
func (c *Claim) Ack(ctx context.Context) error {
	if !c.settled.CompareAndSwap(false, true) {
		return ErrClaimSettled
	}
	c.queue.events.OnAck(c.lease.Task())
	return c.queue.backend.Ack(ctx, c.lease.Token())
}

// Nack reports failure; the queue requeues or dead-letters per policy.
func (c *Claim) Nack(ctx context.Context, reason error) error {
	if !c.settled.CompareAndSwap(false, true) {
		return ErrClaimSettled
	}
	c.queue.events.OnNack(c.lease.Task(), reason)
	return c.queue.route(ctx, c.lease, reason)
}

// Dequeue blocks until a task can be claimed or timeout elapses, polling the
// backend. Returns ErrQueueEmpty on timeout, ErrQueueClosed after Close, or
// ctx.Err() on cancellation.
func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) (*Claim, error) {
	if q.closed.Load() {
		return nil, ErrQueueClosed
	}

	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		lease, err := q.backend.Claim(ctx, q.visibility)
		switch {
		case err == nil:
			q.events.OnClaim(lease)
			return &Claim{queue: q, lease: lease}, nil
		case errors.Is(err, ErrQueueEmpty):
			// fall through to wait/retry
		default:
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrQueueEmpty
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(defaultDequeuePoll, remaining)):
		}
	}
}

// Stream returns a yielder of claims, dequeuing in a background goroutine
// until ctx is canceled, the queue closes, or timeout elapses with no work.
// The caller controls the stream lifetime through ctx and must settle every
// claim it receives.
func (q *Queue) Stream(ctx context.Context, timeout time.Duration, buffer int) (*yielder.Yielder[*Claim], error) {
	if q.closed.Load() {
		return nil, ErrQueueClosed
	}

	feed := make(chan *Claim, buffer)
	go func() {
		defer close(feed)
		for {
			claim, err := q.Dequeue(ctx, timeout)
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
				_ = claim.Nack(ctx, ctx.Err())
				return
			case feed <- claim:
			}
		}
	}()

	stream, err := yielder.New[*Claim](ctx,
		yielder.WithBuffer[*Claim](buffer),
		yielder.WithTimeout[*Claim](timeout),
		yielder.WithInputChannel[*Claim](feed),
	)
	if err != nil {
		// yielder.New only fails when ctx is already done; the producer
		// observes the same ctx, sends nothing, and closes feed.
		return nil, err
	}
	return stream, nil
}

// Redrive moves a dead-lettered task back onto the queue, identified by task
// ID. Returns ErrLeaseNotFound when the DLQ has no such task.
func (q *Queue) Redrive(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.closed.Load() {
		return ErrQueueClosed
	}

	dead, err := q.deadLetter.List(ctx, 0)
	if err != nil {
		return err
	}
	var target Task
	for _, entry := range dead {
		if entry.Task.ID() == id {
			target = entry.Task
			break
		}
	}
	if target == nil {
		return ErrLeaseNotFound
	}

	if err := q.backend.Enqueue(ctx, target); err != nil {
		return err
	}
	q.events.OnEnqueue(target)
	return q.deadLetter.Remove(ctx, id)
}

// DeadLetterLen reports the number of dead-lettered tasks.
func (q *Queue) DeadLetterLen(ctx context.Context) (int, error) {
	return q.deadLetter.Len(ctx)
}
