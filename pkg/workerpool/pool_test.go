package workerpool

import (
	"context"
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

	job := 1
	s.Require().ErrorIs(pool.Submit(&job), ErrPoolShutdown)
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
		job := i
		s.Require().NoError(pool.Submit(&job))
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
		job := i
		go func() { _ = pool.Submit(&job) }()
	}

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == 4
	}, 3*time.Second, 10*time.Millisecond, "expected pool to scale up to MaxSize")
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
		job := i
		go func() { _ = pool.Submit(&job) }()
	}

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() > 1
	}, 3*time.Second, 20*time.Millisecond, "expected pool to scale up first")

	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == 1
	}, 5*time.Second, 20*time.Millisecond, "expected pool to scale down to MinSize")
}
