package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	testWorkers        = 2
	testBacklog        = 16
	testRateLimit      = 1000
	testSubmitTimeout  = 2 * time.Second
	testScenarioBudget = 5 * time.Second
	preShutdownWarmup  = 10 * time.Millisecond
)

type (
	PoolTestSuite struct {
		suite.Suite
	}

	testJob interface {
		Sum(ctx context.Context) (int, error)
	}

	testJobImpl struct {
		arg1, arg2 int
	}
)

func (job *testJobImpl) Sum(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return job.arg1 + job.arg2, nil
}

func TestPoolSuite(t *testing.T) {
	suite.Run(t, new(PoolTestSuite))
}

func (s *PoolTestSuite) newPool(
	workers, backlog int,
	h HandlerFunc[testJob],
	opts ...PoolOptionFunc[testJob],
) *WorkerPool[testJob] {
	s.T().Helper()
	cfg := &Config{
		Mode:      ModeFixedSize,
		Backlog:   backlog,
		RateLimit: testRateLimit,
		ClaimsConfig: ClaimsConfig{
			Name:          "test",
			Size:          workers,
			SubmitTimeout: testSubmitTimeout,
		},
	}
	allOpts := append(
		[]PoolOptionFunc[testJob]{
			WithConfig[testJob](cfg),
			WithHandler[testJob](h),
			WithEvents[testJob](NewNoopEvents[testJob]()),
		},
		opts...,
	)

	pool, err := New[testJob](context.Background(), allOpts...)
	s.Require().NoError(err)

	return pool
}

type recordingEvents struct {
	NoopEvents[testJob]
	mu         sync.Mutex
	failedErrs []error
	okCount    atomic.Int32
}

func (e *recordingEvents) JobFailed(err error, _ testJob) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failedErrs = append(e.failedErrs, err)
}

func (e *recordingEvents) JobOk(_ testJob) {
	e.okCount.Add(1)
}

func (e *recordingEvents) failed() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]error, len(e.failedErrs))
	copy(out, e.failedErrs)
	return out
}

func (s *PoolTestSuite) TestInvalidConfig() {
	validCfg := func() *Config {
		return &Config{
			Mode:         ModeFixedSize,
			Backlog:      testBacklog,
			RateLimit:    testRateLimit,
			ClaimsConfig: ClaimsConfig{Size: testWorkers, SubmitTimeout: testSubmitTimeout},
		}
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name    string
		ctx     context.Context
		opts    []PoolOptionFunc[testJob]
		wantErr error
	}{
		{
			name:    "nil context",
			ctx:     nil,
			opts:    []PoolOptionFunc[testJob]{WithConfig[testJob](validCfg()), WithHandler[testJob](NoopHandler[testJob])},
			wantErr: ErrInvalidPool,
		},
		{
			name:    "already canceled context",
			ctx:     canceledCtx,
			opts:    []PoolOptionFunc[testJob]{WithConfig[testJob](validCfg()), WithHandler[testJob](NoopHandler[testJob])},
			wantErr: context.Canceled,
		},
		{
			name:    "nil handler",
			ctx:     context.Background(),
			opts:    []PoolOptionFunc[testJob]{WithConfig[testJob](validCfg())},
			wantErr: ErrInvalidPool,
		},
		{
			name:    "nil config",
			ctx:     context.Background(),
			opts:    []PoolOptionFunc[testJob]{WithHandler[testJob](NoopHandler[testJob])},
			wantErr: ErrInvalidPool,
		},
		{
			name: "invalid mode",
			ctx:  context.Background(),
			opts: func() []PoolOptionFunc[testJob] {
				cfg := validCfg()
				cfg.Mode = Mode(99)
				return []PoolOptionFunc[testJob]{WithConfig[testJob](cfg), WithHandler[testJob](NoopHandler[testJob])}
			}(),
			wantErr: ErrInvalidPool,
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			pool, err := New[testJob](tc.ctx, tc.opts...)
			s.Require().Error(err)
			s.Require().ErrorIs(err, tc.wantErr)
			s.Nil(pool)
		})
	}
}

func (s *PoolTestSuite) TestSubmitRejection() {
	s.Run("nil ctx", func() {
		pool := s.newPool(testWorkers, testBacklog, NoopHandler[testJob])
		defer pool.Shutdown()
		var nilCtx context.Context
		err := pool.Submit(nilCtx, &testJobImpl{1, 0})
		s.Require().ErrorIs(err, ErrNilCtx)
	})

	s.Run("after Shutdown", func() {
		pool := s.newPool(testWorkers, testBacklog, NoopHandler[testJob])
		pool.Shutdown()
		err := pool.Submit(context.Background(), &testJobImpl{1, 0})
		s.Require().ErrorIs(err, ErrPoolShutdown)
	})

	s.Run("after GracefulShutdown", func() {
		pool := s.newPool(testWorkers, testBacklog, NoopHandler[testJob])
		pool.GracefulShutdown()
		err := pool.Submit(context.Background(), &testJobImpl{1, 0})
		s.Require().ErrorIs(err, ErrPoolShutdown)
	})
}

func (s *PoolTestSuite) TestGracefulShutdownDrainsBacklog() {
	const jobs = 8
	release := make(chan struct{})
	var processed atomic.Int32
	h := func(ctx JobAware[testJob]) error {
		<-release
		processed.Add(1)
		return nil
	}

	pool := s.newPool(testWorkers, jobs, h)
	ctx := context.Background()
	for i := range jobs {
		s.Require().NoError(pool.Submit(ctx, &testJobImpl{i + 1, 0}))
	}

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()

	close(release)

	select {
	case <-done:
	case <-time.After(testScenarioBudget):
		s.FailNow("GracefulShutdown did not complete within budget")
	}

	s.Equal(int32(jobs), processed.Load())
}

func (s *PoolTestSuite) TestHardShutdownCancelsInFlight() {
	const workers = 3
	started := make(chan struct{}, workers)
	var canceled atomic.Int32
	h := func(ctx JobAware[testJob]) error {
		started <- struct{}{}
		<-ctx.Done()
		canceled.Add(1)
		return ctx.Err()
	}

	pool := s.newPool(workers, workers, h)
	ctx := context.Background()
	for i := range workers {
		s.Require().NoError(pool.Submit(ctx, &testJobImpl{i + 1, 0}))
	}

	for range workers {
		select {
		case <-started:
		case <-time.After(testScenarioBudget):
			s.FailNow("worker did not start")
		}
	}

	done := make(chan struct{})
	go func() {
		pool.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(testScenarioBudget):
		s.FailNow("Shutdown did not complete within budget")
	}

	s.Equal(int32(workers), canceled.Load())
}

func (s *PoolTestSuite) TestHandlerPanicRecovered() {
	const firstPayload = 1
	const secondPayload = 2
	var sawPanicJob atomic.Bool
	h := func(ctx JobAware[testJob]) error {
		sum, err := ctx.Job().Sum(ctx)
		if err != nil {
			return err
		}
		if sum == firstPayload {
			sawPanicJob.Store(true)
			panic("handler panic on purpose")
		}
		return nil
	}

	evs := &recordingEvents{}
	pool := s.newPool(1, testBacklog, h, WithEvents[testJob](evs))
	defer pool.GracefulShutdown()

	ctx := context.Background()
	s.Require().NoError(pool.Submit(ctx, &testJobImpl{firstPayload, 0}))
	s.Require().NoError(pool.Submit(ctx, &testJobImpl{secondPayload, 0}))

	s.Require().Eventually(func() bool {
		return evs.okCount.Load() == 1 && sawPanicJob.Load()
	}, testScenarioBudget, preShutdownWarmup)

	failures := evs.failed()
	s.Require().Len(failures, 1)
	s.Require().ErrorIs(failures[0], ErrWorkerPanic)
}

func (s *PoolTestSuite) TestSubmitDuringShutdownNoPanic() {
	const submitters = 20
	const perSubmitter = 10
	pool := s.newPool(testWorkers, testBacklog, NoopHandler[testJob])

	var wg sync.WaitGroup
	errs := make(chan error, submitters*perSubmitter)
	ctx := context.Background()
	for range submitters {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					errs <- fmt.Errorf("submit panicked: %v", r)
				}
			}()
			for range perSubmitter {
				errs <- pool.Submit(ctx, &testJobImpl{1, 1})
			}
		})
	}

	time.Sleep(preShutdownWarmup)
	pool.Shutdown()
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, ErrPoolShutdown):
		case errors.Is(err, ErrSubmitTimeout):
		case errors.Is(err, context.Canceled):
		default:
			s.Failf("unexpected submit error", "got %v", err)
		}
	}
}

func (s *PoolTestSuite) TestDoubleShutdownIdempotent() {
	pool := s.newPool(testWorkers, testBacklog, NoopHandler[testJob])

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { pool.Shutdown() })
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(testScenarioBudget):
		s.FailNow("double Shutdown hung")
	}
}

func (s *PoolTestSuite) TestJobContextCancelPropagates() {
	observed := make(chan error, 1)
	h := func(ctx JobAware[testJob]) error {
		<-ctx.Done()
		observed <- ctx.Err()
		return nil
	}

	pool := s.newPool(1, testBacklog, h)
	defer pool.GracefulShutdown()

	submitCtx, cancel := context.WithCancel(context.Background())
	s.Require().NoError(pool.Submit(submitCtx, &testJobImpl{1, 0}))

	// Give the worker time to pick up the job before we cancel.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() > 0
	}, testScenarioBudget, preShutdownWarmup)

	time.Sleep(preShutdownWarmup)
	cancel()

	select {
	case err := <-observed:
		s.Require().ErrorIs(err, context.Canceled)
	case <-time.After(testScenarioBudget):
		s.FailNow("handler did not observe caller cancellation")
	}
}
