package workerpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type PoolTestSuite struct {
	suite.Suite
}

func TestPoolSuite(t *testing.T) {
	suite.Run(t, new(PoolTestSuite))
}

func (s *PoolTestSuite) fixedConfig(size int) *Config {
	return &Config{
		Mode:         ModeFixedSize,
		ClaimsConfig: ClaimsConfig{Size: size, SubmitTimeout: time.Second},
		Backlog:      20,
		RateLimit:    100000,
	}
}

func (s *PoolTestSuite) autoscaleConfig(minSize, maxSize int, idle time.Duration) *Config {
	return &Config{
		Mode:          ModeAutoScale,
		MinSize:       minSize,
		MaxSize:       maxSize,
		IdleTimeout:   idle,
		ScaleInterval: 20 * time.Millisecond,
		ClaimsConfig:  ClaimsConfig{SubmitTimeout: 2 * time.Second},
		Backlog:       20,
		RateLimit:     100000,
	}
}

// rateLimitedConfig returns a FixedSize config with a single worker and a
// deliberately tiny RateLimit so that the second Submit reliably parks inside
// the rate limiter's Wait. Tests use this to exercise the cancelation paths
// of SubmitContext.
func (s *PoolTestSuite) rateLimitedConfig(rps float64) *Config {
	return &Config{
		Mode:         ModeFixedSize,
		ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: time.Second},
		Backlog:      10,
		RateLimit:    rps,
	}
}

func (s *PoolTestSuite) TestNew_Validation() {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	testCases := []struct {
		title       string
		ctx         context.Context
		opts        []PoolOptionFunc[int]
		expectedErr error
	}{
		{
			title:       "nil context",
			ctx:         nil,
			opts:        []PoolOptionFunc[int]{WithConfig[int](s.fixedConfig(1)), WithHandler[int](NoopHandler[int])},
			expectedErr: ErrInvalidPool,
		},
		{
			title:       "canceled context",
			ctx:         canceled,
			opts:        []PoolOptionFunc[int]{WithConfig[int](s.fixedConfig(1)), WithHandler[int](NoopHandler[int])},
			expectedErr: context.Canceled,
		},
		{
			title:       "nil handler",
			ctx:         s.T().Context(),
			opts:        []PoolOptionFunc[int]{WithConfig[int](s.fixedConfig(1))},
			expectedErr: ErrInvalidPool,
		},
		{
			title:       "nil config",
			ctx:         s.T().Context(),
			opts:        []PoolOptionFunc[int]{WithHandler[int](NoopHandler[int])},
			expectedErr: ErrInvalidPool,
		},
		{
			title:       "valid fixed",
			ctx:         s.T().Context(),
			opts:        []PoolOptionFunc[int]{WithConfig[int](s.fixedConfig(2)), WithHandler[int](NoopHandler[int])},
			expectedErr: nil,
		},
		{
			title: "valid autoscale",
			ctx:   s.T().Context(),
			opts: []PoolOptionFunc[int]{
				WithConfig[int](s.autoscaleConfig(1, 3, 500*time.Millisecond)),
				WithHandler[int](NoopHandler[int]),
			},
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			pool, err := New[int](tc.ctx, tc.opts...)
			if tc.expectedErr != nil {
				s.Require().ErrorIs(err, tc.expectedErr)
				s.Require().Nil(pool)
				return
			}
			s.Require().NoError(err)
			s.Require().NotNil(pool)
			pool.Close()
		})
	}
}

func (s *PoolTestSuite) TestSubmit_Validation() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(1)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Run("nil job", func() {
		s.Require().ErrorIs(pool.Submit(nil), ErrNilJob)
	})
}

func (s *PoolTestSuite) TestSubmit_PoolShutdown() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(1)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	pool.Close()

	s.Require().ErrorIs(pool.Submit(new(1)), ErrPoolShutdown)
}

// TestSubmitContext_Validation covers the synchronous error paths of
// SubmitContext: nil context, nil job, and a caller context that is already
// canceled before the call.
func (s *PoolTestSuite) TestSubmitContext_Validation() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(1)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()

	testCases := []struct {
		title       string
		ctx         context.Context
		job         *int
		expectedErr error
	}{
		{
			title:       "nil ctx",
			ctx:         nil,
			job:         new(1),
			expectedErr: ErrNilCtx,
		},
		{
			title:       "nil job",
			ctx:         context.Background(),
			job:         nil,
			expectedErr: ErrNilJob,
		},
		{
			title:       "pre-canceled ctx",
			ctx:         preCanceled,
			job:         new(1),
			expectedErr: context.Canceled,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			s.Require().ErrorIs(pool.SubmitContext(tc.ctx, tc.job), tc.expectedErr)
		})
	}
}

// TestSubmitContext_PoolShutdown verifies that SubmitContext returns
// ErrPoolShutdown after Close has been called, regardless of caller context.
func (s *PoolTestSuite) TestSubmitContext_PoolShutdown() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(1)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	pool.Close()

	s.Require().ErrorIs(pool.SubmitContext(context.Background(), new(1)), ErrPoolShutdown)
}

// TestSubmitContext_HappyPath verifies that SubmitContext successfully
// enqueues jobs and that they reach the handler.
func (s *PoolTestSuite) TestSubmitContext_HappyPath() {
	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(2)),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	const n = 10
	for i := 1; i <= n; i++ {
		s.Require().NoError(pool.SubmitContext(s.T().Context(), new(i)))
	}

	s.Require().Eventually(func() bool {
		return processed.Load() == int64(n)
	}, 3*time.Second, 10*time.Millisecond)
}

// TestSubmitContext_CallerCtxCancelDuringLimiterWait parks SubmitContext
// inside the rate limiter's Wait by exhausting a tiny token bucket, then
// cancels the caller's context and asserts the call unblocks with the
// caller's error rather than ErrPoolShutdown.
func (s *PoolTestSuite) TestSubmitContext_CallerCtxCancelDuringLimiterWait() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.rateLimitedConfig(0.01)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Consume the burst (Size=1 → burst=1) so the next Submit must wait.
	s.Require().NoError(pool.SubmitContext(context.Background(), new(1)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- pool.SubmitContext(ctx, new(2))
	}()

	// Give the goroutine a moment to enter limiter.Wait.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		s.Require().ErrorIs(err, context.Canceled)
	case <-time.After(2 * time.Second):
		s.Fail("SubmitContext did not return after caller ctx cancel")
	}
}

// TestSubmitContext_PoolCancelDuringLimiterWait parks SubmitContext inside
// the rate limiter's Wait, then cancels the parent context that backs the
// pool's lifecycle context and asserts the call unblocks with
// ErrPoolShutdown — the pool's cancel takes precedence over the caller's.
func (s *PoolTestSuite) TestSubmitContext_PoolCancelDuringLimiterWait() {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	pool, err := New[int](parentCtx,
		WithConfig[int](s.rateLimitedConfig(0.01)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Consume the burst.
	s.Require().NoError(pool.SubmitContext(context.Background(), new(1)))

	done := make(chan error, 1)
	go func() {
		done <- pool.SubmitContext(context.Background(), new(2))
	}()

	time.Sleep(50 * time.Millisecond)
	parentCancel()

	select {
	case err := <-done:
		s.Require().ErrorIs(err, ErrPoolShutdown)
	case <-time.After(2 * time.Second):
		s.Fail("SubmitContext did not return after pool cancel")
	}
}

// TestFixedSize_JoinsAllWorkers verifies that FixedSize mode subscribes all
// workers on startup, before any job is submitted.
func (s *PoolTestSuite) TestFixedSize_JoinsAllWorkers() {
	const size = 4
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(size)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Require().Equal(size, pool.JoinedCount())
}

// TestFixedSize_ProcessesJobs verifies that a FixedSize pool dispatches a
// batch of submitted jobs to its workers.
func (s *PoolTestSuite) TestFixedSize_ProcessesJobs() {
	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.fixedConfig(3)),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	const n = 20
	for i := 1; i <= n; i++ {
		s.Require().NoError(pool.Submit(new(i)))
	}

	s.Require().Eventually(func() bool {
		return processed.Load() == int64(n)
	}, 3*time.Second, 10*time.Millisecond)
}

// TestAutoScale_InitialMinJoined verifies that AutoScale mode only Joins
// MinSize workers at startup (not MaxSize).
func (s *PoolTestSuite) TestAutoScale_InitialMinJoined() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.autoscaleConfig(2, 5, time.Second)),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Require().Equal(2, pool.JoinedCount())
}

// TestAutoScale_ScaleUpOnDemand verifies that the pool adds workers up to
// MaxSize when the initial Min workers cannot keep up with the load.
func (s *PoolTestSuite) TestAutoScale_ScaleUpOnDemand() {
	release := make(chan struct{})
	handler := func(_ context.Context, _ *int) error {
		<-release
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.autoscaleConfig(1, 4, time.Second)),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer func() {
		close(release)
		pool.Close()
	}()

	s.Require().Equal(1, pool.JoinedCount())

	// Submit a burst without waiting so jobs pile up in the backlog.
	for i := 1; i <= 8; i++ {
		go func() { _ = pool.Submit(new(i)) }()
	}

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == 4
	}, 3*time.Second, 10*time.Millisecond, "expected pool to scale up to MaxSize")
}

// TestAutoScale_BoundsInvariant samples JoinedCount on a 1ms tick during a
// burst of work and asserts that the count never exceeds MaxSize and never
// drops below MinSize after the initial join. Existing scale-up / scale-down
// tests only check the endpoints; this one pins the invariant continuously.
func (s *PoolTestSuite) TestAutoScale_BoundsInvariant() {
	const (
		minSize = 2
		maxSize = 6
	)

	handler := func(_ context.Context, _ *int) error {
		// Slow enough that scale-up actually happens, fast enough that the
		// burst eventually drains and scale-down can fire.
		time.Sleep(time.Millisecond)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minSize,
			MaxSize:       maxSize,
			IdleTimeout:   30 * time.Millisecond,
			ScaleInterval: 5 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: time.Second},
			Backlog:       100,
			RateLimit:     1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Sampling goroutine — independent of the producer so timing pressure
	// on Submit cannot mask a transient bound violation.
	var (
		samplesMu  sync.Mutex
		samples    []int
		sampleStop = make(chan struct{})
		sampleWG   sync.WaitGroup
	)

	sampleWG.Go(func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleStop:
				return
			case <-ticker.C:
				j := pool.JoinedCount()
				samplesMu.Lock()
				samples = append(samples, j)
				samplesMu.Unlock()
			}
		}
	})

	// Producer phase — burst for ~300ms, then go idle for ~300ms so the
	// scaler has both a scale-up and a scale-down window to react in.
	// Tolerate ErrSubmitTimeout: the test's contract is "bounds never
	// violated", not "every submit succeeds". A full backlog just means
	// the scaler *should* have caught up, which is exactly what we want
	// to observe via the sampling goroutine.
	var submitted int
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := pool.Submit(new(1)); err == nil {
			submitted++
		}
	}
	s.Require().Positive(submitted, "producer made no successful submits")

	time.Sleep(300 * time.Millisecond)

	close(sampleStop)
	sampleWG.Wait()

	samplesMu.Lock()
	defer samplesMu.Unlock()
	s.Require().NotEmpty(samples)

	maxObserved, minObserved := samples[0], samples[0]
	for _, v := range samples {
		if v > maxObserved {
			maxObserved = v
		}
		if v < minObserved {
			minObserved = v
		}
	}

	s.Require().LessOrEqual(maxObserved, maxSize, "JoinedCount exceeded MaxSize: max=%d", maxObserved)
	s.Require().GreaterOrEqual(minObserved, minSize, "JoinedCount fell below MinSize: min=%d", minObserved)
	// Sanity: with the burst we should have actually scaled above MinSize
	// at least once, otherwise the test never exercised the upper bound.
	s.Require().Greater(maxObserved, minSize, "scale-up never happened — test did not exercise MaxSize bound")
}

// TestAutoScale_ScaleUpViaDispatchPath verifies that scale-up still happens
// when the scaler ticker is effectively disabled (ScaleInterval set very
// high). The only path that can drive scale-up in that configuration is
// the dispatch retry loop calling pool.requestScale().
func (s *PoolTestSuite) TestAutoScale_ScaleUpViaDispatchPath() {
	release := make(chan struct{})
	defer close(release)

	handler := func(_ context.Context, _ *int) error {
		<-release
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:    ModeAutoScale,
			MinSize: 1,
			MaxSize: 4,
			// IdleTimeout / ScaleInterval set very high so the ticker
			// effectively never fires during this test. Any scale-up that
			// happens must come from dispatch's requestScale() call.
			IdleTimeout:   10 * time.Second,
			ScaleInterval: 5 * time.Second,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 100 * time.Millisecond},
			Backlog:       50,
			RateLimit:     1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Require().Equal(1, pool.JoinedCount())

	// Burst enough work to make sure backlog stays non-empty long enough
	// for the scaler to observe pending > 0 when requestScale wakes it.
	for range 20 {
		s.Require().NoError(pool.Submit(new(1)))
	}

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == 4
	}, 3*time.Second, 20*time.Millisecond, "scale-up via dispatch requestScale path did not reach MaxSize")
}

// TestAutoScale_ScaleDownWhenIdle verifies that workers Leave (and the pool
// shrinks back to MinSize) once they've been idle beyond IdleTimeout.
func (s *PoolTestSuite) TestAutoScale_ScaleDownWhenIdle() {
	handler := func(_ context.Context, _ *int) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](s.autoscaleConfig(1, 4, 100*time.Millisecond)),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Burst jobs concurrently to trigger scale-up.
	for i := 1; i <= 20; i++ {
		go func() { _ = pool.Submit(new(i)) }()
	}

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() > 1
	}, 3*time.Second, 20*time.Millisecond, "expected pool to scale up first")

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == 1
	}, 5*time.Second, 20*time.Millisecond, "expected pool to scale down to MinSize")
}
