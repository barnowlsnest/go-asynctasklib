package workerpool

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	testMinSize             = 1
	testMaxSize             = 4
	testScaleUpStep         = 1
	testIdleHeadroom        = 1
	testScalerInterval      = 100 * time.Millisecond
	testScaleDownCooldown   = 5 * time.Second
	testScaleDownIdlePeriod = 2 * time.Second
)

type ScalerTestSuite struct {
	suite.Suite
}

func TestScalerSuite(t *testing.T) {
	suite.Run(t, new(ScalerTestSuite))
}

func (s *ScalerTestSuite) TestAutoScaleConfigDefaults() {
	cases := []struct {
		name     string
		in       AutoScaleConfig
		expected AutoScaleConfig
	}{
		{
			name: "all zero -> defaults",
			in:   AutoScaleConfig{},
			expected: AutoScaleConfig{
				MinSize:             1,
				MaxSize:             runtime.NumCPU(),
				ScaleUpStep:         1,
				Interval:            100 * time.Millisecond,
				IdleHeadroom:        1,
				ScaleUpCooldown:     0,
				ScaleDownCooldown:   5 * time.Second,
				ScaleDownIdlePeriod: 2 * time.Second,
			},
		},
		{
			name: "explicit values preserved",
			in: AutoScaleConfig{
				MinSize: 2, MaxSize: 8, ScaleUpStep: 3, Interval: time.Second,
				IdleHeadroom: 2, ScaleUpCooldown: time.Second,
				ScaleDownCooldown: 10 * time.Second, ScaleDownIdlePeriod: 4 * time.Second,
			},
			expected: AutoScaleConfig{
				MinSize: 2, MaxSize: 8, ScaleUpStep: 3, Interval: time.Second,
				IdleHeadroom: 2, ScaleUpCooldown: time.Second,
				ScaleDownCooldown: 10 * time.Second, ScaleDownIdlePeriod: 4 * time.Second,
			},
		},
		{
			name: "negative normalized like zero",
			in:   AutoScaleConfig{MinSize: -5, MaxSize: -1},
			expected: AutoScaleConfig{
				MinSize:             1,
				MaxSize:             runtime.NumCPU(),
				ScaleUpStep:         1,
				Interval:            100 * time.Millisecond,
				IdleHeadroom:        1,
				ScaleUpCooldown:     0,
				ScaleDownCooldown:   5 * time.Second,
				ScaleDownIdlePeriod: 2 * time.Second,
			},
		},
	}

	for i := range cases {
		tc := cases[i]
		s.Run(tc.name, func() {
			got := tc.in
			got.applyDefaults()
			s.Require().Equal(tc.expected, got)
		})
	}
}

func (s *ScalerTestSuite) TestAutoScaleConfigValidate() {
	cases := []struct {
		name        string
		in          AutoScaleConfig
		expectedErr bool
	}{
		{name: "min below max ok", in: AutoScaleConfig{MinSize: 1, MaxSize: 4}, expectedErr: false},
		{name: "min equals max ok", in: AutoScaleConfig{MinSize: 4, MaxSize: 4}, expectedErr: false},
		{name: "min above max invalid", in: AutoScaleConfig{MinSize: 5, MaxSize: 4}, expectedErr: true},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			err := tc.in.validate()
			if tc.expectedErr {
				s.Require().Error(err)
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

func (s *ScalerTestSuite) TestDecide() {
	// A non-zero ScaleUpCooldown lets the "blocked by up-cooldown" case actually
	// exercise gating; the other up cases have ~100s elapsed and still scale.
	cfg := AutoScaleConfig{
		MinSize: 1, MaxSize: 4, ScaleUpStep: 1, IdleHeadroom: 1,
		ScaleUpCooldown: 5 * time.Second, ScaleDownCooldown: 5 * time.Second,
		ScaleDownIdlePeriod: 2 * time.Second,
	}
	cases := []struct {
		name         string
		snap         snapshot
		expectedDec  decision
		expectedStep int
	}{
		{
			name:        "scale up: backlog with no idle headroom",
			snap:        snapshot{joined: 1, idle: 0, backlog: 3, now: int64Seconds(100), lastUp: 0, lastDown: 0},
			expectedDec: up, expectedStep: 1,
		},
		{
			name:        "scale up clamped to MaxSize-joined",
			snap:        snapshot{joined: 4, idle: 0, backlog: 3, now: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0, // joined == MaxSize, cannot grow
		},
		{
			name:        "scale up step clamped below configured step",
			snap:        snapshot{joined: 3, idle: 0, backlog: 9, now: int64Seconds(100)},
			expectedDec: up, expectedStep: 1, // MaxSize 4 minus joined 3 leaves room for 1
		},
		{
			name:        "no scale up when idle headroom available",
			snap:        snapshot{joined: 2, idle: 2, backlog: 3, now: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0, // idle(2) > headroom(1)
		},
		{
			name:        "scale up blocked by up-cooldown",
			snap:        snapshot{joined: 1, idle: 0, backlog: 3, now: int64Seconds(100), lastUp: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0,
		},
		{
			name:        "scale down: idle over headroom and empty backlog",
			snap:        snapshot{joined: 3, idle: 3, backlog: 0, now: int64Seconds(100), lastDown: 0},
			expectedDec: down, expectedStep: 0,
		},
		{
			name:        "no scale down at MinSize",
			snap:        snapshot{joined: 1, idle: 1, backlog: 0, now: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0,
		},
		{
			name:        "no scale down when backlog present",
			snap:        snapshot{joined: 3, idle: 3, backlog: 1, now: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0,
		},
		{
			name:        "no scale down within headroom",
			snap:        snapshot{joined: 2, idle: 1, backlog: 0, now: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0, // idle(1) not > headroom(1)
		},
		{
			name:        "scale down blocked by down-cooldown",
			snap:        snapshot{joined: 3, idle: 3, backlog: 0, now: int64Seconds(101), lastDown: int64Seconds(100)},
			expectedDec: hold, expectedStep: 0, // 1s elapsed < 5s cooldown
		},
		{
			name:        "scale down allowed after down-cooldown",
			snap:        snapshot{joined: 3, idle: 3, backlog: 0, now: int64Seconds(106), lastDown: int64Seconds(100)},
			expectedDec: down, expectedStep: 0, // 6s elapsed >= 5s cooldown
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			gotDec, gotStep := decide(tc.snap, cfg)
			s.Require().Equal(tc.expectedDec, gotDec)
			s.Require().Equal(tc.expectedStep, gotStep)
		})
	}
}

// int64Seconds returns n seconds as a unix-nanosecond-scaled int64 for snapshot
// timestamps in tests.
func int64Seconds(n int64) int64 { return n * int64(time.Second) }

type testCandidate struct {
	running    bool
	lastActive int64
}

func (c testCandidate) IsRunning() bool     { return c.running }
func (c testCandidate) LastActiveAt() int64 { return c.lastActive }

func (s *ScalerTestSuite) TestPickVictim() {
	const now = 1_000 * int64(time.Second) // 1000s in nanos
	idlePeriod := 2 * time.Second

	cases := []struct {
		name        string
		candidates  []idleCandidate
		expectedIdx int
		expectedOK  bool
	}{
		{
			name:        "no candidates",
			candidates:  nil,
			expectedIdx: -1, expectedOK: false,
		},
		{
			name: "all running -> none removable",
			candidates: []idleCandidate{
				testCandidate{running: true, lastActive: 0},
				testCandidate{running: true, lastActive: 0},
			},
			expectedIdx: -1, expectedOK: false,
		},
		{
			name: "idle but not long enough",
			candidates: []idleCandidate{
				testCandidate{running: false, lastActive: now - int64(time.Second)}, // 1s < 2s
			},
			expectedIdx: -1, expectedOK: false,
		},
		{
			name: "single removable idle worker",
			candidates: []idleCandidate{
				testCandidate{running: false, lastActive: now - int64(3*time.Second)},
			},
			expectedIdx: 0, expectedOK: true,
		},
		{
			name: "picks longest-idle among removable",
			candidates: []idleCandidate{
				testCandidate{running: false, lastActive: now - int64(3*time.Second)},
				testCandidate{running: false, lastActive: now - int64(9*time.Second)}, // oldest
				testCandidate{running: false, lastActive: now - int64(5*time.Second)},
			},
			expectedIdx: 1, expectedOK: true,
		},
		{
			name: "skips running and too-recent, picks the eligible one",
			candidates: []idleCandidate{
				testCandidate{running: true, lastActive: now - int64(100*time.Second)},
				testCandidate{running: false, lastActive: now - int64(time.Second)}, // too recent
				testCandidate{running: false, lastActive: now - int64(4*time.Second)},
			},
			expectedIdx: 2, expectedOK: true,
		},
	}

	for i := range cases {
		tc := cases[i]
		s.Run(tc.name, func() {
			idx, ok := pickVictim(tc.candidates, now, idlePeriod)
			s.Require().Equal(tc.expectedOK, ok)
			s.Require().Equal(tc.expectedIdx, idx)
		})
	}
}

// autoScaleConfig builds a Config for an auto-scaling test pool.
func autoScaleConfig(minSize, maxSize int) *Config {
	return &Config{
		Mode:      ModeAutoScale,
		Backlog:   testBacklog,
		RateLimit: testRateLimit,
		ClaimsConfig: ClaimsConfig{
			Name:          "scaler-test",
			SubmitTimeout: testSubmitTimeout,
		},
		AutoScale: AutoScaleConfig{
			MinSize:             minSize,
			MaxSize:             maxSize,
			ScaleUpStep:         testScaleUpStep,
			IdleHeadroom:        testIdleHeadroom,
			Interval:            testScalerInterval,
			ScaleUpCooldown:     0,
			ScaleDownCooldown:   testScaleDownCooldown,
			ScaleDownIdlePeriod: testScaleDownIdlePeriod,
		},
	}
}

// manualClock is a test clock the test advances explicitly. It is seeded to real
// wall-clock nanos so deltas against worker.LastActiveAt (real time.Now) hold.
type manualClock struct {
	nanos atomic.Int64
}

func newManualClock() *manualClock {
	c := &manualClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}

func (c *manualClock) now() int64 { return c.nanos.Load() }

//nolint:unused // advance is used by Task 5 scale-down tests
func (c *manualClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// newAutoPool builds a ModeAutoScale pool whose scaler is driven by the given
// tick channel and clock instead of a real ticker.
func (s *ScalerTestSuite) newAutoPool(
	minSize, maxSize int,
	h HandlerFunc[testJob],
	tick <-chan time.Time,
	clock *manualClock,
) *WorkerPool[testJob] {
	s.T().Helper()
	pool, err := New[testJob](
		context.Background(),
		WithConfig[testJob](autoScaleConfig(minSize, maxSize)),
		WithHandler[testJob](h),
		WithEvents[testJob](NewNoopEvents[testJob]()),
		withScalerClock[testJob](tick, clock.now),
	)
	s.Require().NoError(err)
	return pool
}

func (s *ScalerTestSuite) TestAutoScaleStartsAtMinSize() {
	tick := make(chan time.Time)
	clock := newManualClock()
	pool := s.newAutoPool(testMinSize, testMaxSize, NoopHandler[testJob], tick, clock)
	defer pool.Shutdown()

	s.Require().Equal(testMinSize, pool.JoinedCount())
}

func (s *ScalerTestSuite) TestAutoScaleScalesUpUnderBacklog() {
	const submitted = 8
	release := make(chan struct{})
	h := func(ctx JobAware[testJob]) error {
		<-release
		return nil
	}

	tick := make(chan time.Time)
	clock := newManualClock()
	pool := s.newAutoPool(testMinSize, testMaxSize, h, tick, clock)

	ctx := context.Background()
	for i := range submitted {
		s.Require().NoError(pool.Submit(ctx, &testJobImpl{i + 1, 0}))
	}

	// Drive ticks until the pool fills to MaxSize.
	s.Require().Eventually(func() bool {
		select {
		case tick <- time.Now():
		default:
		}
		return pool.JoinedCount() == testMaxSize
	}, testScenarioBudget, preShutdownWarmup)

	// Never exceeds MaxSize even with more ticks.
	for range 3 {
		tick <- time.Now()
	}
	s.Require().Equal(testMaxSize, pool.JoinedCount())

	close(release)
	pool.GracefulShutdown()
}

func (s *ScalerTestSuite) TestAutoScaleShutdownStopsScaler() {
	cases := []struct {
		name string
		stop func(*WorkerPool[testJob])
	}{
		{name: "hard shutdown", stop: func(p *WorkerPool[testJob]) { p.Shutdown() }},
		{name: "graceful shutdown", stop: func(p *WorkerPool[testJob]) { p.GracefulShutdown() }},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			tick := make(chan time.Time)
			clock := newManualClock()
			pool := s.newAutoPool(testMinSize, testMaxSize, NoopHandler[testJob], tick, clock)

			done := make(chan struct{})
			go func() {
				tc.stop(pool)
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(testScenarioBudget):
				s.FailNow("shutdown did not complete; scaler likely stuck")
			}
		})
	}
}
