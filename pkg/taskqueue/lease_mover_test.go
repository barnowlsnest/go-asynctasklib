package taskqueue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type testMovingDeadLetter struct {
	*MemoryDeadLetter
	backend Backend
	moveErr error
	mu      sync.Mutex
	moved   int
}

func newTestMovingDeadLetter(backend Backend) *testMovingDeadLetter {
	return &testMovingDeadLetter{MemoryDeadLetter: NewMemoryDeadLetter(), backend: backend}
}

func (d *testMovingDeadLetter) MovesFrom(backend Backend) bool { return backend == d.backend }

func (d *testMovingDeadLetter) MoveLease(ctx context.Context, lease Lease, reason error) error {
	if d.moveErr != nil {
		return d.moveErr
	}
	if err := d.backend.Nack(ctx, lease.Token(), false); err != nil {
		return err
	}
	if err := d.Add(ctx, lease.Task(), reason); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.moved++
	return nil
}

func (d *testMovingDeadLetter) movedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.moved
}

type LeaseMoverSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *LeaseMoverSuite) SetupTest() { s.ctx = context.Background() }

func TestLeaseMoverSuite(t *testing.T) {
	suite.Run(t, new(LeaseMoverSuite))
}

func (s *LeaseMoverSuite) nackUntilDead(queue *Queue) error {
	s.T().Helper()
	var err error
	for range testMaxAttempts {
		claim, dequeueErr := queue.Dequeue(s.ctx, testVisibility)
		s.Require().NoError(dequeueErr)
		err = claim.Nack(s.ctx, errDead)
	}
	return err
}

func (s *LeaseMoverSuite) TestNackDeadLettersThroughMoverOnlyWhenPaired() {
	cases := []struct {
		name          string
		paired        bool
		expectedMoved int
	}{
		{name: "paired backend uses MoveLease", paired: true, expectedMoved: 1},
		{name: "unpaired backend falls back to Add and Nack", paired: false, expectedMoved: 0},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			backend := NewMemoryBackend(ModePriority)
			moverBackend := Backend(NewMemoryBackend(ModePriority))
			if tc.paired {
				moverBackend = backend
			}
			dlq := newTestMovingDeadLetter(moverBackend)
			queue := newTestQueue(&s.Suite, s.ctx, WithBackend(backend), WithDeadLetter(dlq))
			defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

			s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))
			s.Require().NoError(s.nackUntilDead(queue))

			s.Equal(tc.expectedMoved, dlq.movedCount())
			dlqLen, err := dlq.Len(s.ctx)
			s.Require().NoError(err)
			s.Equal(1, dlqLen)
			backendLen, err := backend.Len(s.ctx)
			s.Require().NoError(err)
			s.Zero(backendLen)
		})
	}
}

func (s *LeaseMoverSuite) TestReaperDeadLettersThroughMover() {
	backend := NewMemoryBackend(ModePriority)
	dlq := newTestMovingDeadLetter(backend)
	queue := newTestQueue(&s.Suite, s.ctx, WithBackend(backend), WithDeadLetter(dlq), WithMaxAttempts(1))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))
	claim, err := queue.Dequeue(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(1, claim.attemptForTest())

	s.Require().Eventually(func() bool { return dlq.movedCount() == 1 }, time.Second, testReapInterval)
	s.ErrorIs(claim.Ack(s.ctx), ErrLeaseNotFound)
}

func (s *LeaseMoverSuite) TestMoveLeaseErrorIsReturnedFromNack() {
	backend := NewMemoryBackend(ModePriority)
	dlq := newTestMovingDeadLetter(backend)
	dlq.moveErr = ErrLeaseNotFound
	queue := newTestQueue(&s.Suite, s.ctx, WithBackend(backend), WithDeadLetter(dlq))
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	s.Require().NoError(queue.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	s.ErrorIs(s.nackUntilDead(queue), ErrLeaseNotFound)
	dlqLen, err := dlq.Len(s.ctx)
	s.Require().NoError(err)
	s.Zero(dlqLen)
}
