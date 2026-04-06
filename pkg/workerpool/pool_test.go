package workerpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// ---------------------------------------------------------------------------
// PoolFixedSuite
// ---------------------------------------------------------------------------

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
	p, err := New(s.T().Context(), s.fixedConfig(3), NoopHandler[int])
	s.Require().NoError(err)

	s.Require().Len(p.active, 3)
	s.Require().Len(p.claims.subscribers, 3)

	for id, w := range p.active {
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

	p, err := New(s.T().Context(), s.fixedConfig(3), handler)
	s.Require().NoError(err)

	n := 20
	subs := make([]<-chan error, 0, n)
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
			case <-sub:
			case <-time.After(time.Second):
				s.Fail("submission timed out")
			}
		})
	}
	wg.Wait()

	for _, sub := range subs {
		s.Require().NoError(<-sub)
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

	p, err := New(s.T().Context(), s.fixedConfig(2), handler)
	s.Require().NoError(err)

	const n = 4
	for i := range n {
		sub, submitErr := p.Submit(new(i))
		s.Require().NoError(submitErr)
		s.Require().NoError(<-sub)
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

func (s *PoolFixedSuite) TestFixed_Shutdown() {
	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		processed.Add(1)
		return nil
	}

	p, err := New(s.T().Context(), s.fixedConfig(3), handler)
	s.Require().NoError(err)

	s.Require().NoError(p.Shutdown(time.Second))
	s.Require().Equal(0, p.activeWorkers())

	_, submitErr := p.Submit(new(1))
	s.Require().ErrorIs(submitErr, ErrPoolShutdown)
}

func (s *PoolFixedSuite) TestFixed_ShutdownIdempotent() {
	p, err := New(s.T().Context(), s.fixedConfig(2), NoopHandler[int])
	s.Require().NoError(err)

	s.Require().NoError(p.Shutdown(time.Second))
	s.Require().NoError(p.Shutdown(time.Second))
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
		{
			title: "autoscale nil config",
			ctx:   validCtx,
			cfg: &Config[int]{
				Rate: 10, MaxSubmitRetries: 1,
				Mode: ModeAutoScale,
			},
			expectedErr: ErrInvalidConfig,
		},
		{
			title: "autoscale MinSize < 1",
			ctx:   validCtx,
			cfg: &Config[int]{
				Rate: 10, MaxSubmitRetries: 1,
				Mode:      ModeAutoScale,
				AutoScale: &AutoScaleConfig{MinSize: 0, MaxSize: 5, ScaleUpThreshold: 0.8},
			},
			expectedErr: ErrInvalidConfig,
		},
		{
			title: "autoscale MaxSize < MinSize",
			ctx:   validCtx,
			cfg: &Config[int]{
				Rate: 10, MaxSubmitRetries: 1,
				Mode:      ModeAutoScale,
				AutoScale: &AutoScaleConfig{MinSize: 5, MaxSize: 2, ScaleUpThreshold: 0.8},
			},
			expectedErr: ErrInvalidConfig,
		},
		{
			title: "autoscale ScaleUpThreshold out of range",
			ctx:   validCtx,
			cfg: &Config[int]{
				Rate: 10, MaxSubmitRetries: 1,
				Mode:      ModeAutoScale,
				AutoScale: &AutoScaleConfig{MinSize: 1, MaxSize: 5, ScaleUpThreshold: 0},
			},
			expectedErr: ErrInvalidConfig,
		},
	}

	for _, tc := range cases {
		s.Run(tc.title, func() {
			p, err := New(tc.ctx, tc.cfg, NoopHandler[int])
			s.Require().ErrorIs(err, tc.expectedErr)
			s.Require().Nil(p)
		})
	}
}

// ---------------------------------------------------------------------------
// PoolAutoScaleSuite
// ---------------------------------------------------------------------------

type PoolAutoScaleSuite struct {
	suite.Suite
}

func TestPoolAutoScaleSuite(t *testing.T) {
	suite.Run(t, new(PoolAutoScaleSuite))
}

func (s *PoolAutoScaleSuite) autoScaleConfig(minSize, maxSize int) *Config[int] {
	return &Config[int]{
		Name:             "autoscale-test",
		Rate:             1000,
		MaxSubmitRetries: 5,
		SubmitTimeout:    time.Second,
		Mode:             ModeAutoScale,
		BaseRetryDelay:   100 * time.Millisecond,
		MaxRetryDelay:    500 * time.Millisecond,
		AutoScale: &AutoScaleConfig{
			MinSize:          minSize,
			MaxSize:          maxSize,
			ScaleInterval:    50 * time.Millisecond,
			ScaleUpThreshold: 0.8,
			ScaleDownIdle:    200 * time.Millisecond,
			Cooldown:         100 * time.Millisecond,
			StepUp:           1,
			StepDown:         1,
		},
	}
}

func (s *PoolAutoScaleSuite) TestAutoScale_StartsAtMinSize() {
	p, err := New(s.T().Context(), s.autoScaleConfig(2, 5), NoopHandler[int])
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(time.Second)) }()

	s.Require().Equal(2, p.activeWorkers())
	p.mu.Lock()
	s.Require().Len(p.parked, 3)
	p.mu.Unlock()
}

func (s *PoolAutoScaleSuite) TestAutoScale_ScalesUpUnderLoad() {
	blocker := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-blocker
		return nil
	}

	cfg := s.autoScaleConfig(1, 4)
	p, err := New(s.T().Context(), cfg, handler)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(time.Second)) }()

	s.Require().Equal(1, p.activeWorkers())

	// Submit enough jobs to saturate — the single worker will block.
	for i := range 4 {
		_, _ = p.Submit(new(i))
	}

	// Wait for scale-up.
	s.Require().Eventually(func() bool {
		return p.activeWorkers() > 1
	}, 2*time.Second, 25*time.Millisecond, "pool should scale up under load")

	close(blocker)
}

func (s *PoolAutoScaleSuite) TestAutoScale_RespectsMaxSize() {
	blocker := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-blocker
		return nil
	}

	cfg := s.autoScaleConfig(1, 3)
	p, err := New(s.T().Context(), cfg, handler)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(10 * time.Second)) }()

	for i := range 10 {
		sub, err := p.Submit(new(i))
		s.Require().NoError(err)
		s.Require().NotNil(sub)
		s.Require().NoError(<-sub)
	}

	// Let the scaler run several ticks.
	time.Sleep(time.Second)
	s.Require().LessOrEqual(p.activeWorkers(), 3)

	close(blocker)
}

func (s *PoolAutoScaleSuite) TestAutoScale_ScalesDownWhenIdle() {
	blocker := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-blocker
		return nil
	}

	cfg := s.autoScaleConfig(1, 4)
	p, err := New(s.T().Context(), cfg, handler)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(time.Second)) }()

	// Saturate to trigger scale-up.
	for i := range 4 {
		_, _ = p.Submit(new(i))
	}

	s.Require().Eventually(func() bool {
		return p.activeWorkers() > 1
	}, 2*time.Second, 25*time.Millisecond)

	// Release all jobs — pool becomes idle.
	close(blocker)

	// Wait for scale-down toward MinSize.
	s.Require().Eventually(func() bool {
		return p.activeWorkers() == 1
	}, 3*time.Second, 50*time.Millisecond, "pool should scale down to MinSize when idle")
}

func (s *PoolAutoScaleSuite) TestAutoScale_RespectsMinSize() {
	cfg := s.autoScaleConfig(2, 5)
	p, err := New(s.T().Context(), cfg, NoopHandler[int])
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(time.Second)) }()

	// Wait well past ScaleDownIdle — should never go below MinSize.
	time.Sleep(cfg.AutoScale.ScaleDownIdle * 3)
	s.Require().GreaterOrEqual(p.activeWorkers(), 2)
}

func (s *PoolAutoScaleSuite) TestAutoScale_ShutdownWhileScaling() {
	blocker := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-blocker
		return nil
	}

	cfg := s.autoScaleConfig(1, 4)
	p, err := New(s.T().Context(), cfg, handler)
	s.Require().NoError(err)

	for i := range 4 {
		_, _ = p.Submit(new(i))
	}

	// Give the scaler a moment to start scaling up.
	time.Sleep(200 * time.Millisecond)
	close(blocker)

	err = p.Shutdown(2 * time.Second)
	s.Require().NoError(err)
	s.Require().Equal(0, p.activeWorkers())
}

func (s *PoolAutoScaleSuite) TestAutoScale_ReusesParkedWorkerOnScaleUp() {
	blocker := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-blocker
		return nil
	}

	cfg := s.autoScaleConfig(1, 3)
	p, err := New(s.T().Context(), cfg, handler)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(p.Shutdown(time.Second)) }()

	// Record parked IDs before scale-up.
	p.mu.Lock()
	parkedIDs := make(map[uint64]bool, len(p.parked))
	for id := range p.parked {
		parkedIDs[id] = true
	}
	p.mu.Unlock()
	s.Require().Len(parkedIDs, 2)

	// Saturate to trigger scale-up.
	for i := range 3 {
		_, _ = p.Submit(new(i))
	}

	s.Require().Eventually(func() bool {
		return p.activeWorkers() > 1
	}, 2*time.Second, 25*time.Millisecond)

	// The newly active worker must have been in parked before.
	p.mu.Lock()
	for id := range p.active {
		if id != 1 { // worker 1 was initially active
			s.Require().True(parkedIDs[id], "scaled-up worker %d should have been parked", id)
		}
	}
	p.mu.Unlock()

	close(blocker)
}
