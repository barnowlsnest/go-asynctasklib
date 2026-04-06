package workerpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type PoolFixedSuite struct {
	suite.Suite
}

func TestPoolFixedSuite(t *testing.T) {
	suite.Run(t, new(PoolFixedSuite))
}

func (s *PoolFixedSuite) fixedConfig(size int) *Config[int] {
	return &Config[int]{
		Name:             "fixed-test",
		Rate:             1000,
		Size:             size,
		MaxSubmitRetries: 3,
		SubmitTimeout:    500 * time.Millisecond,
	}
}

// TestFixed_WorkersAreJoinedOnNew is the regression test for the earlier
// Join gap: New used to create workers but never Join them, leaving the
// pool with zero subscribers and Submit silently retrying.
func (s *PoolFixedSuite) TestFixed_WorkersAreJoinedOnNew() {
	p, err := New[int](s.T().Context(), s.fixedConfig(3), NoopHandler[int])
	s.Require().NoError(err)

	s.Require().Len(p.workers, 3)
	s.Require().Len(p.claims.subscribers, 3)

	for id, w := range p.workers {
		s.Require().Equal(id, w.ID())
		ctx, ok := w.Context()
		s.Require().True(ok, "worker %d must have an active Join context", id)
		s.Require().NotNil(ctx)
	}
}

// TestFixed_SubmitsAreProcessed drives a small batch of jobs through a
// real pool with a fast handler and verifies every submission completes.
func (s *PoolFixedSuite) TestFixed_SubmitsAreProcessed() {
	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		processed.Add(1)
		return nil
	}

	p, err := New[int](s.T().Context(), s.fixedConfig(3), handler)
	s.Require().NoError(err)

	n := 20
	subs := make([]*Submission, 0, n)
	for i := range n {
		sub, submitErr := p.Submit(new(i))
		s.Require().NoError(submitErr)
		s.Require().NotNil(sub)
		subs = append(subs, sub)
	}

	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Go(func() {
			select {
			case <-sub.Done():
			case <-time.After(time.Second):
				s.Fail("submission timed out")
			}
		})
	}
	wg.Wait()

	for _, sub := range subs {
		s.Require().NoError(sub.Err())
	}

	s.Require().Eventually(func() bool {
		return processed.Load() == int64(n)
	}, time.Second, 10*time.Millisecond)
}

// TestFixed_WorkerIDContextValueIsSet verifies that handlers see the
// per-worker ID on the context, which is the contract newWorkerContext
// establishes via CtxWorkerID.
func (s *PoolFixedSuite) TestFixed_WorkerIDContextValueIsSet() {
	seen := make(chan uint64, 4)
	handler := func(ctx context.Context, _ *int) error {
		if wid, ok := ctx.Value(CtxWorkerID).(uint64); ok {
			seen <- wid
		}
		return nil
	}

	p, err := New[int](s.T().Context(), s.fixedConfig(2), handler)
	s.Require().NoError(err)

	const n = 4
	for i := range n {
		sub, submitErr := p.Submit(new(i))
		s.Require().NoError(submitErr)
		<-sub.Done()
		s.Require().NoError(sub.Err())
	}

	collected := make(map[uint64]bool)
	for range n {
		select {
		case id := <-seen:
			collected[id] = true
		case <-time.After(time.Second):
			s.FailNow("timed out waiting for worker id")
		}
	}

	for id := range collected {
		s.Require().NotZero(id)
		s.Require().LessOrEqual(id, uint64(2))
	}
}

// TestFixed_NewRejectsInvalidInput covers every New[T] failure path in a
// single table: invalid parent contexts and invalid Config values both end
// up as an error returned from New before any worker is created.
func (s *PoolFixedSuite) TestFixed_NewRejectsInvalidInput() {
	validCtx := s.T().Context()
	canceledCtx, cancel := context.WithCancel(s.T().Context())
	cancel()

	cases := []struct {
		title       string
		ctx         context.Context
		cfg         *Config[int]
		expectedErr error
	}{
		{
			title:       "nil parent ctx",
			ctx:         nil,
			cfg:         s.fixedConfig(1),
			expectedErr: ErrNilCtx,
		},
		{
			title:       "canceled parent ctx",
			ctx:         canceledCtx,
			cfg:         s.fixedConfig(1),
			expectedErr: context.Canceled,
		},
		{
			title:       "nil config",
			ctx:         validCtx,
			cfg:         nil,
			expectedErr: ErrInvalidConfig,
		},
		{
			title:       "size <= 0",
			ctx:         validCtx,
			cfg:         &Config[int]{Rate: 10, MaxSubmitRetries: 1, Size: 0},
			expectedErr: ErrInvalidConfig,
		},
		{
			title:       "rate <= 0",
			ctx:         validCtx,
			cfg:         &Config[int]{Rate: 0, MaxSubmitRetries: 1, Size: 1},
			expectedErr: ErrInvalidConfig,
		},
		{
			title:       "max submit retries <= 0",
			ctx:         validCtx,
			cfg:         &Config[int]{Rate: 10, MaxSubmitRetries: 0, Size: 1},
			expectedErr: ErrInvalidConfig,
		},
	}

	for _, tc := range cases {
		s.Run(tc.title, func() {
			p, err := New[int](tc.ctx, tc.cfg, NoopHandler[int])
			s.Require().ErrorIs(err, tc.expectedErr)
			s.Require().Nil(p)
		})
	}
}
