package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// pool_e2e_test.go exercises the worker pool end-to-end with real handlers,
// real Events, real contexts, and real timings. No mocks: every piece of the
// pipeline runs the same code paths production callers hit.

type (
	PoolE2ETestSuite struct {
		suite.Suite
	}

	// countingEvents is a production-shaped Events[T] implementation that
	// records observable side effects as atomic counters. It embeds
	// NoopEvents so we only have to override the hooks we care about.
	countingEvents[T any] struct {
		NoopEvents[T]
		started   atomic.Int64
		stopped   atomic.Int64
		jobOk     atomic.Int64
		jobFailed atomic.Int64
	}
)

func (e *countingEvents[T]) WorkerStarted(_ uint64)  { e.started.Add(1) }
func (e *countingEvents[T]) WorkerStopped(_ uint64)  { e.stopped.Add(1) }
func (e *countingEvents[T]) JobOk(_ *T)              { e.jobOk.Add(1) }
func (e *countingEvents[T]) JobFailed(_ error, _ *T) { e.jobFailed.Add(1) }

func TestPoolE2ESuite(t *testing.T) {
	suite.Run(t, new(PoolE2ETestSuite))
}

// TestFixedSize_ConcurrentProducersAggregate fans many producers into a
// fixed pool of workers that cooperatively build up a shared aggregate.
// Every job must be delivered exactly once: we verify that via the sum
// of 1..N, which is only correct if no job was lost or duplicated.
func (s *PoolE2ETestSuite) TestFixedSize_ConcurrentProducersAggregate() {
	const (
		workers     = 8
		producers   = 10
		perProducer = 100
		total       = producers * perProducer
	)

	var (
		sum   atomic.Int64
		count atomic.Int64
	)
	handler := func(_ context.Context, job *int) error {
		sum.Add(int64(*job))
		count.Add(1)
		return nil
	}

	events := &countingEvents[int]{}
	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: workers, SubmitTimeout: 5 * time.Second},
			Backlog:      total,
			RateLimit:    1_000_000,
		}),
		WithHandler(handler),
		WithEvents(events),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Sanity: FixedSize should subscribe every worker up front.
	s.Require().Equal(workers, pool.JoinedCount())

	var wg sync.WaitGroup
	for p := range producers {
		wg.Go(func() {
			for i := 1; i <= perProducer; i++ {
				v := p*perProducer + i
				s.Require().NoError(pool.Submit(&v))
			}
		})
	}
	wg.Wait()

	s.Require().Eventually(func() bool {
		return count.Load() == int64(total)
	}, 10*time.Second, 20*time.Millisecond, "not all jobs were processed")

	// Sum of 1..total — any duplicate or missing job breaks this identity.
	expectedSum := int64(total) * int64(total+1) / 2
	s.Require().Equal(expectedSum, sum.Load())
	s.Require().Equal(int64(total), events.jobOk.Load())
	s.Require().Zero(events.jobFailed.Load())
}

// TestFixedSize_ErrorsAndPanicsKeepFlowing submits a mix of successful,
// error-returning, and panicking jobs. The pool must recover from each
// kind of failure, report them via Events, and still drain the full
// batch.
func (s *PoolE2ETestSuite) TestFixedSize_ErrorsAndPanicsKeepFlowing() {
	const total = 300

	var (
		oks    atomic.Int64
		errs   atomic.Int64
		panics atomic.Int64
	)

	errSkip := errors.New("skip")
	handler := func(_ context.Context, job *int) error {
		switch {
		case *job%7 == 0:
			panics.Add(1)
			panic("boom")
		case *job%3 == 0:
			errs.Add(1)
			return errSkip
		default:
			oks.Add(1)
			return nil
		}
	}

	events := &countingEvents[int]{}
	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: 5 * time.Second},
			Backlog:      total,
			RateLimit:    1_000_000,
		}),
		WithHandler(handler),
		WithEvents(events),
	)
	s.Require().NoError(err)
	defer pool.Close()

	for i := 1; i <= total; i++ {
		job := i
		s.Require().NoError(pool.Submit(&job))
	}

	s.Require().Eventually(func() bool {
		return oks.Load()+errs.Load()+panics.Load() == int64(total)
	}, 10*time.Second, 20*time.Millisecond, "pool did not drain full batch")

	// Events cross-check the per-handler counters.
	s.Require().Equal(oks.Load(), events.jobOk.Load())
	s.Require().Equal(errs.Load()+panics.Load(), events.jobFailed.Load())
	s.Require().Positive(panics.Load(), "expected at least one panic to exercise recover")
}

// TestAutoScale_SpikeThenIdle simulates a bursty traffic pattern: the pool
// starts at MinSize, is hit with a short spike that forces it to scale up,
// and is then left idle long enough to scale back down to MinSize.
func (s *PoolE2ETestSuite) TestAutoScale_SpikeThenIdle() {
	const (
		minWorkers = 1
		maxWorkers = 5
		jobs       = 80
	)

	handler := func(_ context.Context, _ *int) error {
		// Each job takes long enough for the backlog to build visibly
		// behind the current worker set, nudging the autoscaler.
		time.Sleep(30 * time.Millisecond)
		return nil
	}

	events := &countingEvents[int]{}
	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minWorkers,
			MaxSize:       maxWorkers,
			IdleTimeout:   150 * time.Millisecond,
			ScaleInterval: 15 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 3 * time.Second},
			Backlog:       jobs,
			RateLimit:     1_000_000,
		}),
		WithHandler(handler),
		WithEvents(events),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Require().Equal(minWorkers, pool.JoinedCount())

	var wg sync.WaitGroup
	for i := 1; i <= jobs; i++ {
		job := i
		wg.Go(func() { _ = pool.Submit(&job) })
	}

	// The scaler should grow the pool above MinSize while the burst is
	// in flight.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() > minWorkers
	}, 3*time.Second, 15*time.Millisecond, "pool did not scale up during spike")

	wg.Wait()

	// Once the burst drains and the IdleTimeout elapses the pool should
	// drop back to MinSize.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == minWorkers
	}, 5*time.Second, 20*time.Millisecond, "pool did not scale down after spike")

	// The scale-up implies multiple WorkerStarted events beyond the
	// initial MinSize subscription.
	s.Require().Greater(events.started.Load(), int64(minWorkers), "expected more worker starts than MinSize")
}

// TestAutoScale_SustainedBurstSaturatesMax drives enough sustained load
// that the autoscaler must reach MaxSize. Every submitted job should
// still be processed exactly once before the pool returns to MinSize.
func (s *PoolE2ETestSuite) TestAutoScale_SustainedBurstSaturatesMax() {
	const (
		minWorkers = 1
		maxWorkers = 6
		jobs       = 400
	)

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		time.Sleep(15 * time.Millisecond)
		processed.Add(1)
		return nil
	}

	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minWorkers,
			MaxSize:       maxWorkers,
			IdleTimeout:   200 * time.Millisecond,
			ScaleInterval: 10 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 5 * time.Second},
			Backlog:       jobs,
			RateLimit:     1_000_000,
		}),
		WithHandler(handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	var wg sync.WaitGroup
	for i := 1; i <= jobs; i++ {
		job := i
		wg.Go(func() { _ = pool.Submit(&job) })
	}

	// The autoscaler must eventually saturate MaxSize under sustained load.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == maxWorkers
	}, 5*time.Second, 10*time.Millisecond, "pool did not saturate MaxSize")

	wg.Wait()

	// Every submission should be processed.
	s.Require().Eventually(func() bool {
		return processed.Load() == int64(jobs)
	}, 10*time.Second, 20*time.Millisecond, "not all jobs were processed")

	// And once the queue drains we return to MinSize.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == minWorkers
	}, 5*time.Second, 20*time.Millisecond, "pool did not return to MinSize")
}

// TestGracefulCloseRejectsNewWork verifies the shutdown path end to end:
// an in-flight stream of jobs plus a deliberate Close, followed by a post-
// close submission that must be rejected without panicking.
func (s *PoolE2ETestSuite) TestGracefulCloseRejectsNewWork() {
	var processed atomic.Int64
	handler := func(ctx context.Context, _ *int) error {
		select {
		case <-time.After(10 * time.Millisecond):
			processed.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: 500 * time.Millisecond},
			Backlog:      200,
			RateLimit:    1_000_000,
		}),
		WithHandler(handler),
	)
	s.Require().NoError(err)

	// Drive a steady producer so Close races against in-flight work.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			job := i
			_ = pool.Submit(&job)
		}
	})

	// Let the pipeline warm up, then ask the producer to stop and join it
	// before closing the pool — guarantees no in-flight chan-send races
	// with shutdown.
	time.Sleep(80 * time.Millisecond)
	close(stop)
	wg.Wait()

	s.Require().NotPanics(func() { pool.Close() })
	s.Require().Positive(processed.Load(), "expected some jobs to be processed before close")

	// Post-close submissions must be rejected.
	late := 999
	s.Require().ErrorIs(pool.Submit(&late), ErrPoolShutdown)

	// Double close should also be safe thanks to sync.Once.
	s.Require().NotPanics(func() { pool.Close() })
}
