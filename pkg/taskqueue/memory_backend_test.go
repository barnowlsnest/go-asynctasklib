package taskqueue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	testVisibility = 50 * time.Millisecond
)

// fakeTask is a minimal Task for backend/queue tests. It records whether Do
// ran but performs no real work.
type fakeTask struct {
	id       uint64
	seq      uint64
	priority Priority
	doErr    error
}

func (t *fakeTask) Do(context.Context) error { return t.doErr }
func (t *fakeTask) ID() uint64               { return t.id }
func (t *fakeTask) Seq() uint64              { return t.seq }
func (t *fakeTask) Priority() Priority       { return t.priority }

type MemoryBackendSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *MemoryBackendSuite) SetupTest() { s.ctx = context.Background() }

func TestMemoryBackendSuite(t *testing.T) {
	suite.Run(t, new(MemoryBackendSuite))
}

func (s *MemoryBackendSuite) claimIDs(backend *MemoryBackend, count int) []uint64 {
	ids := make([]uint64, 0, count)
	for range count {
		lease, err := backend.Claim(s.ctx, testVisibility)
		s.Require().NoError(err)
		ids = append(ids, lease.Task().ID())
	}
	return ids
}

func (s *MemoryBackendSuite) TestClaimOrdering() {
	cases := []struct {
		name    string
		mode    Mode
		enqueue []*fakeTask
		want    []uint64
	}{
		{
			name: "priority high first then fifo within equal priority",
			mode: ModePriority,
			enqueue: []*fakeTask{
				{id: 1, seq: 1, priority: PriorityLow},
				{id: 2, seq: 2, priority: PriorityHigh},
				{id: 3, seq: 3, priority: PriorityHigh},
				{id: 4, seq: 4, priority: PrioritySuper},
			},
			want: []uint64{4, 2, 3, 1},
		},
		{
			name: "fifo ignores priority and orders by seq",
			mode: ModeFIFO,
			enqueue: []*fakeTask{
				{id: 1, seq: 1, priority: PrioritySuper},
				{id: 2, seq: 2, priority: PriorityLow},
				{id: 3, seq: 3, priority: PriorityHigh},
			},
			want: []uint64{1, 2, 3},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			backend := NewMemoryBackend(tc.mode)
			for _, task := range tc.enqueue {
				s.Require().NoError(backend.Enqueue(s.ctx, task))
			}
			s.Equal(tc.want, s.claimIDs(backend, len(tc.want)))
		})
	}
}

func (s *MemoryBackendSuite) TestClaimEmptyReturnsErrQueueEmpty() {
	backend := NewMemoryBackend(ModePriority)
	_, err := backend.Claim(s.ctx, testVisibility)
	s.ErrorIs(err, ErrQueueEmpty)
}

func (s *MemoryBackendSuite) TestClaimIncrementsAttemptAndSetsDeadline() {
	backend := NewMemoryBackend(ModePriority)
	s.Require().NoError(backend.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))

	before := time.Now()
	lease, err := backend.Claim(s.ctx, testVisibility)
	s.Require().NoError(err)
	s.Equal(1, lease.Attempt())
	s.NotEmpty(lease.Token())
	s.WithinDuration(before.Add(testVisibility), lease.Deadline(), testVisibility)
}

func (s *MemoryBackendSuite) TestEnqueueNilTask() {
	backend := NewMemoryBackend(ModePriority)
	s.ErrorIs(backend.Enqueue(s.ctx, nil), ErrTaskNil)
}

func (s *MemoryBackendSuite) TestAckRemovesTask() {
	backend := NewMemoryBackend(ModePriority)
	s.Require().NoError(backend.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))
	lease, err := backend.Claim(s.ctx, testVisibility)
	s.Require().NoError(err)

	s.Require().NoError(backend.Ack(s.ctx, lease.Token()))

	length, err := backend.Len(s.ctx)
	s.Require().NoError(err)
	s.Zero(length)
	s.ErrorIs(backend.Ack(s.ctx, lease.Token()), ErrLeaseNotFound)
}

func (s *MemoryBackendSuite) TestNack() {
	cases := []struct {
		name       string
		requeue    bool
		wantLen    int
		wantClaims int
	}{
		{name: "requeue returns task to ready", requeue: true, wantLen: 1, wantClaims: 1},
		{name: "drop removes task", requeue: false, wantLen: 0, wantClaims: 0},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			backend := NewMemoryBackend(ModePriority)
			s.Require().NoError(backend.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))
			lease, err := backend.Claim(s.ctx, testVisibility)
			s.Require().NoError(err)

			s.Require().NoError(backend.Nack(s.ctx, lease.Token(), tc.requeue))

			length, err := backend.Len(s.ctx)
			s.Require().NoError(err)
			s.Equal(tc.wantLen, length)

			if tc.wantClaims == 1 {
				reclaim, err := backend.Claim(s.ctx, testVisibility)
				s.Require().NoError(err)
				s.Equal(2, reclaim.Attempt(), "attempt preserved across requeue")
			}
		})
	}
}

func (s *MemoryBackendSuite) TestNackUnknownToken() {
	backend := NewMemoryBackend(ModePriority)
	s.ErrorIs(backend.Nack(s.ctx, "missing", true), ErrLeaseNotFound)
}

func (s *MemoryBackendSuite) TestReapExpiredReturnsHeldLeases() {
	backend := NewMemoryBackend(ModePriority)
	s.Require().NoError(backend.Enqueue(s.ctx, &fakeTask{id: 1, seq: 1}))
	lease, err := backend.Claim(s.ctx, testVisibility)
	s.Require().NoError(err)

	// Before expiry: nothing reaped.
	fresh, err := backend.ReapExpired(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Empty(fresh)

	// After the deadline: the lease is returned but left held (still leased).
	expired, err := backend.ReapExpired(s.ctx, lease.Deadline().Add(time.Millisecond))
	s.Require().NoError(err)
	s.Require().Len(expired, 1)
	s.Equal(lease.Token(), expired[0].Token())

	length, err := backend.Len(s.ctx)
	s.Require().NoError(err)
	s.Zero(length, "reap does not requeue; the lease is still held")
}
