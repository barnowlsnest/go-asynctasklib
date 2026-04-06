package workerpool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// fakeScalerPool is a deterministic stub satisfying scalerPool[int].
// No goroutines, no channels — just counters for evaluate() to drive.
type fakeScalerPool struct {
	ctx_       context.Context
	active_    int
	pending_   int
	joinCalls  int
	leaveCalls int
	joinErr    error
	leaveErr   error
}

func (f *fakeScalerPool) poolCtx() context.Context { return f.ctx_ }
func (f *fakeScalerPool) activeWorkers() int       { return f.active_ }
func (f *fakeScalerPool) pendingClaims() int       { return f.pending_ }

func (f *fakeScalerPool) joinOne() error {
	if f.joinErr != nil {
		return f.joinErr
	}
	f.joinCalls++
	f.active_++
	f.pending_++
	return nil
}

func (f *fakeScalerPool) leaveLRU(_ time.Duration) error {
	if f.leaveErr != nil {
		return f.leaveErr
	}
	f.leaveCalls++
	f.active_--
	f.pending_--
	return nil
}

type ScalerSuite struct {
	suite.Suite
}

func TestScalerSuite(t *testing.T) {
	suite.Run(t, new(ScalerSuite))
}

func (s *ScalerSuite) defaultCfg() *AutoScaleConfig {
	return &AutoScaleConfig{
		MinSize:          1,
		MaxSize:          5,
		ScaleInterval:    50 * time.Millisecond,
		ScaleUpThreshold: 0.8,
		ScaleDownIdle:    200 * time.Millisecond,
		Cooldown:         100 * time.Millisecond,
		StepUp:           1,
		StepDown:         1,
	}
}

func (s *ScalerSuite) TestEvaluate() {
	t0 := time.Now()

	cases := []struct {
		title          string
		active         int
		pending        int
		idleSinceDelta time.Duration // 0 means zero-value (not idle)
		lastScaleDelta time.Duration // 0 means zero-value (never scaled)
		expectJoins    int
		expectLeaves   int
	}{
		{
			title:       "scale up when util >= threshold",
			active:      2,
			pending:     0, // util = 1.0
			expectJoins: 1,
		},
		{
			title:       "no scale up at MaxSize",
			active:      5,
			pending:     0,
			expectJoins: 0,
		},
		{
			title:          "no scale up during cooldown",
			active:         2,
			pending:        0,
			lastScaleDelta: 50 * time.Millisecond, // < 100ms cooldown
			expectJoins:    0,
		},
		{
			title:          "scale down after idle dwell",
			active:         3,
			pending:        3,                      // all idle
			idleSinceDelta: 300 * time.Millisecond, // > 200ms ScaleDownIdle
			expectLeaves:   1,
		},
		{
			title:          "no scale down at MinSize",
			active:         1,
			pending:        1,
			idleSinceDelta: 300 * time.Millisecond,
			expectLeaves:   0,
		},
		{
			title:          "no scale down during cooldown",
			active:         3,
			pending:        3,
			idleSinceDelta: 300 * time.Millisecond,
			lastScaleDelta: 50 * time.Millisecond,
			expectLeaves:   0,
		},
		{
			title:          "no scale down before idle dwell elapses",
			active:         3,
			pending:        3,
			idleSinceDelta: 50 * time.Millisecond, // < 200ms
			expectLeaves:   0,
		},
		{
			title:       "no action when partially busy",
			active:      4,
			pending:     2, // partial utilization, below threshold
			expectJoins: 0,
		},
	}

	for _, tc := range cases {
		s.Run(tc.title, func() {
			pool := &fakeScalerPool{
				ctx_:     context.Background(),
				active_:  tc.active,
				pending_: tc.pending,
			}

			cfg := s.defaultCfg()
			sc := newScaler[int](pool, cfg)

			if tc.idleSinceDelta > 0 {
				sc.idleSince = t0.Add(-tc.idleSinceDelta)
			}
			if tc.lastScaleDelta > 0 {
				sc.lastScaleAt = t0.Add(-tc.lastScaleDelta)
			}

			sc.evaluate(t0)

			s.Require().Equal(tc.expectJoins, pool.joinCalls, "join calls")
			s.Require().Equal(tc.expectLeaves, pool.leaveCalls, "leave calls")
		})
	}
}

func (s *ScalerSuite) TestEvaluate_IdleSinceResetsWhenLoadReturns() {
	t0 := time.Now()
	pool := &fakeScalerPool{
		ctx_:     context.Background(),
		active_:  3,
		pending_: 3, // all idle
	}
	cfg := s.defaultCfg()
	sc := newScaler[int](pool, cfg)
	sc.idleSince = t0.Add(-100 * time.Millisecond)

	// Load returns — not fully idle anymore.
	pool.pending_ = 1
	sc.evaluate(t0)

	s.Require().True(sc.idleSince.IsZero(), "idleSince should reset when load returns")
}

func (s *ScalerSuite) TestEvaluate_CooldownPreventsFlappping() {
	t0 := time.Now()
	pool := &fakeScalerPool{
		ctx_:     context.Background(),
		active_:  2,
		pending_: 0, // high util
	}
	cfg := s.defaultCfg()
	sc := newScaler[int](pool, cfg)

	// First evaluate: scales up.
	sc.evaluate(t0)
	s.Require().Equal(1, pool.joinCalls)

	// Second evaluate within cooldown: no scale.
	sc.evaluate(t0.Add(50 * time.Millisecond))
	s.Require().Equal(1, pool.joinCalls, "should not scale during cooldown")

	// Third evaluate after cooldown: scales up again.
	pool.pending_ = 0 // still high util
	sc.evaluate(t0.Add(200 * time.Millisecond))
	s.Require().Equal(2, pool.joinCalls, "should scale after cooldown")
}
