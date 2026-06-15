package taskqueue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	testReapInterval = 20 * time.Millisecond
	testMaxAttempts  = 2
)

func newTestQueue(s *suite.Suite, ctx context.Context, opts ...Option) *Queue {
	base := []Option{
		WithVisibilityTimeout(testVisibility),
		WithReapInterval(testReapInterval),
		WithMaxAttempts(testMaxAttempts),
	}
	queue, err := New(ctx, append(base, opts...)...)
	s.Require().NoError(err)
	return queue
}

// recordingEvents counts each hook for assertions. Embeds NoopQueueEvents so
// only the hooks under test are overridden. Each count has a single-value
// accessor so tests assert exactly what they care about.
type recordingEvents struct {
	NoopQueueEvents
	mu         sync.Mutex
	enqueued   int
	retried    int
	deadLetter int
}

func (e *recordingEvents) OnEnqueue(Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enqueued++
}

func (e *recordingEvents) OnRetry(Task, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retried++
}

func (e *recordingEvents) OnDeadLetter(Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deadLetter++
}

func (e *recordingEvents) enqueuedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enqueued
}

func (e *recordingEvents) retriedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.retried
}

func (e *recordingEvents) deadLetterCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deadLetter
}

func (c *Claim) attemptForTest() int { return c.lease.Attempt() }

type QueueSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *QueueSuite) SetupTest() { s.ctx = context.Background() }

func TestQueueSuite(t *testing.T) {
	suite.Run(t, new(QueueSuite))
}

func (s *QueueSuite) TestNewNilContext() {
	var nilCtx context.Context
	_, err := New(nilCtx)
	s.ErrorIs(err, ErrInvalidQueue)
}

func (s *QueueSuite) TestNewCanceledContext() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(ctx)
	s.ErrorIs(err, context.Canceled)
}

func (s *QueueSuite) TestCloseIsIdempotentAndStopsReaper() {
	queue := newTestQueue(&s.Suite, s.ctx)
	s.Require().NoError(queue.Close(s.ctx))
	s.Require().NoError(queue.Close(s.ctx))
}

func (s *QueueSuite) TestEnqueueAfterCloseFails() {
	queue := newTestQueue(&s.Suite, s.ctx)
	s.Require().NoError(queue.Close(s.ctx))
	s.ErrorIs(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}), ErrQueueClosed)
}

func (s *QueueSuite) TestEnqueueDelegatesAndEmits() {
	events := &recordingEvents{}
	backend := NewMemoryBackend(ModePriority)
	queue := newTestQueue(&s.Suite, s.ctx, WithBackend(backend), WithEvents(events))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	length, err := backend.Len(s.ctx)
	s.Require().NoError(err)
	s.Equal(1, length)
	s.Equal(1, events.enqueuedCount())
	s.ErrorIs(queue.Enqueue(s.ctx, nil), ErrTaskNil)
}

func (s *QueueSuite) TestDequeueReturnsClaimInPriorityOrder() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1, priority: PriorityLow}))
	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 2, seq: 2, priority: PriorityHigh}))

	claim, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(uint64(2), claim.Task().ID())
	s.Require().NoError(claim.Ack(s.ctx))

	s.ErrorIs(claim.Ack(s.ctx), ErrClaimSettled)
}

func (s *QueueSuite) TestDequeueEmptyTimesOut() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	const waitFor = 30 * time.Millisecond
	start := time.Now()
	_, err := queue.Dequeue(s.ctx, waitFor)
	s.ErrorIs(err, ErrQueueEmpty)
	s.GreaterOrEqual(time.Since(start), waitFor)
}

func (s *QueueSuite) TestDequeueAfterCloseFails() {
	queue := newTestQueue(&s.Suite, s.ctx)
	s.Require().NoError(queue.Close(s.ctx))
	_, err := queue.Dequeue(s.ctx, testVisibility)
	s.ErrorIs(err, ErrQueueClosed)
}

func (s *QueueSuite) TestDequeueRespectsContextCancel() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(context.Background())) }()

	ctx, cancel := context.WithCancel(s.ctx)
	cancel()
	_, err := queue.Dequeue(ctx, testVisibility)
	s.ErrorIs(err, context.Canceled)
}

func (s *QueueSuite) TestNackRequeuesUntilMaxThenDeadLetters() {
	events := &recordingEvents{}
	backend := NewMemoryBackend(ModePriority)
	dlq := NewMemoryDeadLetter()
	queue := newTestQueue(&s.Suite, s.ctx,
		WithBackend(backend), WithDeadLetter(dlq), WithEvents(events))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	// testMaxAttempts == 2: first Nack requeues, second Nack dead-letters.
	first, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(1, first.attemptForTest())
	s.Require().NoError(first.Nack(s.ctx, errDead))

	second, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(2, second.attemptForTest())
	s.Require().NoError(second.Nack(s.ctx, errDead))

	backendLen, err := backend.Len(s.ctx)
	s.Require().NoError(err)
	s.Zero(backendLen, "exhausted task is no longer claimable")

	dlqLen, err := dlq.Len(s.ctx)
	s.Require().NoError(err)
	s.Equal(1, dlqLen)

	s.Equal(1, events.retriedCount())
	s.Equal(1, events.deadLetterCount())
}

func (s *QueueSuite) TestReaperRequeuesExpiredLeaseForRetry() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	claim, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(1, claim.attemptForTest())

	s.Require().Eventually(func() bool {
		next, err := queue.Dequeue(s.ctx, testVisibility)
		if err != nil {
			return false
		}
		return next.attemptForTest() == 2
	}, time.Second, testReapInterval)
}

func (s *QueueSuite) TestReaperDeadLettersExhaustedExpiredLease() {
	dlq := NewMemoryDeadLetter()
	queue := newTestQueue(&s.Suite, s.ctx,
		WithDeadLetter(dlq),
		WithMaxAttempts(1))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	claim, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(1, claim.attemptForTest())

	s.Require().Eventually(func() bool {
		length, err := dlq.Len(s.ctx)
		return err == nil && length == 1
	}, time.Second, testReapInterval)
}

func (s *QueueSuite) TestStreamYieldsClaims() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	const total = 3
	for id := uint64(1); id <= total; id++ {
		s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: id, seq: id}))
	}

	streamCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	const streamTimeout = 200 * time.Millisecond
	stream, err := queue.Stream(streamCtx, streamTimeout, total)
	s.Require().NoError(err)

	got := 0
	for claim := range stream.Results() {
		s.Require().NoError(claim.Ack(s.ctx))
		got++
		if got == total {
			cancel()
		}
	}
	s.Equal(total, got)
}

func (s *QueueSuite) TestRedriveMovesTaskFromDLQBackToQueue() {
	dlq := NewMemoryDeadLetter()
	backend := NewMemoryBackend(ModePriority)
	queue := newTestQueue(&s.Suite, s.ctx, WithBackend(backend), WithDeadLetter(dlq))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	const taskID = uint64(9)
	s.Require().NoError(dlq.Add(s.ctx, &fakeTask{id: taskID, seq: 1}, errDead))

	s.Require().NoError(queue.Redrive(s.ctx, taskID))

	dlqLen, err := queue.DeadLetterLen(s.ctx)
	s.Require().NoError(err)
	s.Zero(dlqLen)

	claim, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(taskID, claim.Task().ID())
	s.Require().NoError(claim.Ack(s.ctx))
}

func (s *QueueSuite) TestRedriveUnknownID() {
	queue := newTestQueue(&s.Suite, s.ctx)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()
	s.ErrorIs(queue.Redrive(s.ctx, 404), ErrLeaseNotFound)
}
