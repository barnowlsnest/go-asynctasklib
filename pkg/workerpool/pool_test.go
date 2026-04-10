package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type PoolTestSuite struct {
	suite.Suite
}

func TestPoolSuite(t *testing.T) {
	suite.Run(t, new(PoolTestSuite))
}

// --------------- mock ---------------

type poolEventsMock struct {
	mock.Mock
}

func (m *poolEventsMock) WorkerStarted(id uint64)                 { m.Called(id) }
func (m *poolEventsMock) WorkerStopped(id uint64)                 { m.Called(id) }
func (m *poolEventsMock) JobFailed(err error, job *int)           { m.Called(err, job) }
func (m *poolEventsMock) JobOk(job *int)                          { m.Called(job) }
func (m *poolEventsMock) Subscribed(id uint64)                    { m.Called(id) }
func (m *poolEventsMock) SubscribeFailed(err error, id uint64)    { m.Called(err, id) }
func (m *poolEventsMock) Unsubscribed(id uint64)                  { m.Called(id) }
func (m *poolEventsMock) UnsubscribeFailed(err error, id uint64)  { m.Called(err, id) }
func (m *poolEventsMock) LeaveTimeout(id uint64, d time.Duration) { m.Called(id, d) }

// --------------- helpers ---------------

func (s *PoolTestSuite) defaultPoolConfig() *Config {
	return &Config{
		ClaimsConfig: ClaimsConfig{
			Size:          10,
			SubmitTimeout: time.Second,
		},
		Backlog:   10,
		RateLimit: 10000,
	}
}

func (s *PoolTestSuite) newPool(ctx context.Context, cfg *Config, handler HandlerFunc[int]) *WorkerPool[int] {
	s.T().Helper()
	pool, err := New[int](ctx,
		WithConfig[int](cfg),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	return pool
}

// --------------- TestNew ---------------

func (s *PoolTestSuite) TestNew() {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	validOpts := func() []PoolOptionFunc[int] {
		return []PoolOptionFunc[int]{
			WithConfig[int](s.defaultPoolConfig()),
			WithHandler[int](NoopHandler[int]),
		}
	}

	testCases := []struct {
		title       string
		ctx         context.Context
		opts        []PoolOptionFunc[int]
		expectedErr error
	}{
		{
			title:       "nil context",
			ctx:         nil,
			opts:        validOpts(),
			expectedErr: ErrInvalidPool,
		},
		{
			title:       "canceled context",
			ctx:         canceledCtx,
			opts:        validOpts(),
			expectedErr: context.Canceled,
		},
		{
			title: "nil handler",
			ctx:   s.T().Context(),
			opts: []PoolOptionFunc[int]{
				WithConfig[int](s.defaultPoolConfig()),
			},
			expectedErr: ErrInvalidPool,
		},
		{
			title: "nil config",
			ctx:   s.T().Context(),
			opts: []PoolOptionFunc[int]{
				WithHandler[int](NoopHandler[int]),
			},
			expectedErr: ErrInvalidPool,
		},
		{
			title:       "valid",
			ctx:         s.T().Context(),
			opts:        validOpts(),
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			pool, err := New[int](tc.ctx, tc.opts...)
			switch tc.expectedErr {
			case nil:
				s.Require().NoError(err)
				s.Require().NotNil(pool)
				pool.Close()
			default:
				s.Require().ErrorIs(err, tc.expectedErr)
				s.Require().Nil(pool)
			}
		})
	}
}

// --------------- TestSubmit_Validation ---------------

func (s *PoolTestSuite) TestSubmit_Validation() {
	testCases := []struct {
		title       string
		job         *int
		expectedErr error
	}{
		{
			title:       "nil job",
			job:         nil,
			expectedErr: ErrNilJob,
		},
	}

	pool := s.newPool(s.T().Context(), s.defaultPoolConfig(), NoopHandler[int])
	defer pool.Close()

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			s.Require().ErrorIs(pool.Submit(tc.job), tc.expectedErr)
		})
	}
}

// --------------- TestSubmit_PoolShutdown ---------------

func (s *PoolTestSuite) TestSubmit_PoolShutdown() {
	pool := s.newPool(s.T().Context(), s.defaultPoolConfig(), NoopHandler[int])
	pool.Close()

	job := 1
	s.Require().ErrorIs(pool.Submit(&job), ErrPoolShutdown)
}

// --------------- TestSubmit_Overflow ---------------

func (s *PoolTestSuite) TestSubmit_Overflow() {
	testCases := []struct {
		title       string
		backlog     int
		workerCount int
		blockWorker bool
		submitCount int
	}{
		{
			title:       "backlog full, no workers",
			backlog:     1,
			workerCount: 0,
			submitCount: 20,
		},
		{
			title:       "backlog full, all workers busy",
			backlog:     1,
			workerCount: 1,
			blockWorker: true,
			submitCount: 20,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			ctx := s.T().Context()
			cfg := &Config{
				ClaimsConfig: ClaimsConfig{
					Size:          max(tc.workerCount, 1),
					SubmitTimeout: 100 * time.Millisecond,
				},
				Backlog:   tc.backlog,
				RateLimit: 100000,
			}
			pool := s.newPool(ctx, cfg, NoopHandler[int])
			defer pool.Close()

			blockCh := make(chan struct{})
			defer close(blockCh)

			for i := range tc.workerCount {
				var handler HandlerFunc[int]
				if tc.blockWorker {
					handler = func(_ context.Context, _ *int) error {
						<-blockCh
						return nil
					}
				} else {
					handler = NoopHandler[int]
				}
				w, err := NewWorker[int](&WorkerConfig[int]{ID: uint64(i + 1), HandlerFunc: handler})
				s.Require().NoError(err)
				s.Require().NoError(w.Join(ctx, pool.availableWorkers))
			}

			// Keep the single worker busy so it stops advertising on claims
			if tc.blockWorker && tc.workerCount > 0 {
				job := 0
				s.Require().NoError(pool.Submit(&job))
				time.Sleep(50 * time.Millisecond)
			}

			errs := make(chan error, tc.submitCount)
			var wg sync.WaitGroup
			for i := 1; i <= tc.submitCount; i++ {
				job := i
				wg.Go(func() {
					errs <- pool.Submit(&job)
				})
			}
			wg.Wait()
			close(errs)

			var timeouts int
			for err := range errs {
				if errors.Is(err, ErrSubmitTimeout) {
					timeouts++
				}
			}

			s.Require().Greater(timeouts, 0, "expected at least one submit timeout")
		})
	}
}

// --------------- TestSubmit_ContextCanceled ---------------

func (s *PoolTestSuite) TestSubmit_ContextCanceled() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cfg := &Config{
		ClaimsConfig: ClaimsConfig{
			Size:          1,
			SubmitTimeout: 10 * time.Second,
		},
		Backlog:   1,
		RateLimit: 100000,
	}
	pool := s.newPool(ctx, cfg, NoopHandler[int])
	defer pool.Close()

	// Fill pipeline: job1 → backlog → listen drains and blocks in submit (no workers)
	// job2 → fills backlog
	job1, job2 := 1, 2
	s.Require().NoError(pool.Submit(&job1))
	time.Sleep(20 * time.Millisecond)
	s.Require().NoError(pool.Submit(&job2))

	// job3 blocks on full backlog
	errCh := make(chan error, 1)
	go func() {
		job3 := 3
		errCh <- pool.Submit(&job3)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		s.Require().ErrorIs(err, context.Canceled)
	case <-time.After(5 * time.Second):
		s.FailNow("submit did not return after context cancellation")
	}
}

// --------------- TestSubmit_HappyPath ---------------

func (s *PoolTestSuite) TestSubmit_HappyPath() {
	ctx := s.T().Context()

	arrivals := make(chan int, 10)
	handler := func(_ context.Context, job *int) error {
		arrivals <- *job
		return nil
	}

	events := &poolEventsMock{}
	events.On("Subscribed", mock.AnythingOfType("uint64")).Return()
	events.On("WorkerStarted", mock.AnythingOfType("uint64")).Return()
	events.On("WorkerStopped", mock.AnythingOfType("uint64")).Return()
	events.On("Unsubscribed", mock.AnythingOfType("uint64")).Return()
	events.On("JobOk", mock.AnythingOfType("*int")).Return()

	cfg := &Config{
		ClaimsConfig: ClaimsConfig{
			Size:          2,
			SubmitTimeout: time.Second,
		},
		Backlog:   10,
		RateLimit: 10000,
	}
	pool, err := New[int](ctx,
		WithConfig[int](cfg),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	workers := make([]*Worker[int], 0, 2)
	for i := uint64(1); i <= 2; i++ {
		w, wErr := NewWorker[int](&WorkerConfig[int]{ID: i, HandlerFunc: handler, Events: events})
		s.Require().NoError(wErr)
		s.Require().NoError(w.Join(ctx, pool.availableWorkers))
		workers = append(workers, w)
	}

	const jobCount = 5
	for i := 1; i <= jobCount; i++ {
		job := i
		s.Require().NoError(pool.Submit(&job))
	}

	received := make(map[int]bool)
	for range jobCount {
		select {
		case v := <-arrivals:
			received[v] = true
		case <-time.After(5 * time.Second):
			s.FailNow("timeout waiting for job")
		}
	}

	for i := 1; i <= jobCount; i++ {
		s.Require().True(received[i], "job %d not received", i)
	}

	for _, w := range workers {
		s.Require().NoError(w.Leave(pool.availableWorkers, time.Second))
	}
	pool.Close()

	events.AssertNumberOfCalls(s.T(), "JobOk", jobCount)
}
