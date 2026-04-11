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

// PoolLifecycleTestSuite covers the dangerous-edge transitions of a pool:
// shutdown racing with concurrent submitters, double Close, and the
// blocking-handler shutdown path. The intent is to make every visible
// outcome a contract, not an implementation accident.
type PoolLifecycleTestSuite struct {
	suite.Suite
}

func TestPoolLifecycleSuite(t *testing.T) {
	suite.Run(t, new(PoolLifecycleTestSuite))
}

// TestSubmit_RacesClose hammers the pool with N concurrent submitters while
// another goroutine calls Close. Every Submit must return one of the
// documented outcomes — never panic, never "send on closed channel". This
// is the test the comment at pool.go's Close() points at: "callers may
// still be inside Submit after reject flipped, and closing would race with
// an in-flight chan send."
func (s *PoolLifecycleTestSuite) TestSubmit_RacesClose() {
	const (
		producers       = 32
		jobsPerProducer = 200
	)

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: time.Second},
			Backlog:      64,
			RateLimit:    1_000_000,
		}),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)

	var (
		wg          sync.WaitGroup
		okCount     atomic.Int64
		shutdownErr atomic.Int64
		ctxErr      atomic.Int64
		timeoutErr  atomic.Int64
		otherErr    atomic.Int64
	)

	for range producers {
		wg.Go(func() {
			for i := range jobsPerProducer {
				switch err := pool.Submit(new(i)); {
				case err == nil:
					okCount.Add(1)
				case errors.Is(err, ErrPoolShutdown):
					shutdownErr.Add(1)
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					ctxErr.Add(1)
				case errors.Is(err, ErrSubmitTimeout):
					timeoutErr.Add(1)
				default:
					otherErr.Add(1)
				}
			}
		})
	}

	// Race Close against the producers. Sleep just long enough for some
	// submits to land before flipping the pool into rejecting mode.
	time.Sleep(20 * time.Millisecond)
	pool.Close()

	wg.Wait()

	total := okCount.Load() + shutdownErr.Load() + ctxErr.Load() + timeoutErr.Load() + otherErr.Load()
	s.Require().Equal(int64(producers*jobsPerProducer), total, "every submit must return")
	s.Require().Zero(otherErr.Load(), "no unexpected error class")
	// We expect at least *some* submits to succeed (the early ones) and at
	// least *some* to be rejected (after Close). Otherwise the test isn't
	// actually exercising the race.
	s.Require().Positive(okCount.Load(), "no submits succeeded — race window did not open")
	s.Require().Positive(shutdownErr.Load(), "no submits saw ErrPoolShutdown — Close did not happen mid-race")
}

// TestClose_Idempotent verifies that calling Close more than once is safe
// and that subsequent Submit calls all return ErrPoolShutdown.
func (s *PoolLifecycleTestSuite) TestClose_Idempotent() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 2, SubmitTimeout: time.Second},
			Backlog:      4,
			RateLimit:    100000,
		}),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)

	pool.Close()
	pool.Close()
	pool.Close()

	s.Require().ErrorIs(pool.Submit(new(1)), ErrPoolShutdown)
}

// TestClose_ConcurrentClose verifies that Close called from N goroutines is
// safe — only one of them runs the underlying shutdown sequence (sync.Once)
// and all return without error.
func (s *PoolLifecycleTestSuite) TestClose_ConcurrentClose() {
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 2, SubmitTimeout: time.Second},
			Backlog:      4,
			RateLimit:    100000,
		}),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			pool.Close()
		})
	}
	wg.Wait()

	s.Require().ErrorIs(pool.Submit(new(1)), ErrPoolShutdown)
}

// leaveTimeoutEvents records how many times LeaveTimeout fires during a
// shutdown. Used by the blocking-handler test to assert the pool gives up
// on a stuck worker after SubmitTimeout instead of hanging Close forever.
type leaveTimeoutEvents struct {
	NoopEvents[int]
	fired atomic.Int32
}

func (l *leaveTimeoutEvents) LeaveTimeout(_ uint64, _ time.Duration) {
	l.fired.Add(1)
}

// TestClose_BlockingHandlerFiresLeaveTimeout verifies that Close on a pool
// whose only worker is stuck inside a handler that does not respect ctx
// returns within SubmitTimeout (plus slack) and that Events.LeaveTimeout
// fires for the stuck worker. This is the contract documented on
// Worker.Leave: "It returns ErrWorkerTimeout if the worker does not stop
// within timeout."
func (s *PoolLifecycleTestSuite) TestClose_BlockingHandlerFiresLeaveTimeout() {
	started := make(chan struct{})
	release := make(chan struct{})
	// Make sure the handler unblocks at the end of the test even if an
	// assertion fails — otherwise the worker goroutine leaks past the test
	// boundary and trips goleak.
	defer close(release)

	handler := func(_ context.Context, _ *int) error {
		close(started)
		<-release
		return nil
	}

	events := &leaveTimeoutEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 100 * time.Millisecond},
			Backlog:      4,
			RateLimit:    100000,
		}),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	s.Require().NoError(pool.Submit(new(1)))
	<-started

	closeStart := time.Now()
	pool.Close()
	closeDuration := time.Since(closeStart)

	// SubmitTimeout (100ms) bounds Leave's wait. With one stuck worker,
	// Close should return within roughly that timeout plus scheduler slack.
	s.Require().Less(closeDuration, time.Second, "Close took too long: %v", closeDuration)
	s.Require().GreaterOrEqual(int(events.fired.Load()), 1, "expected LeaveTimeout to fire for the stuck worker")
}

// TestClose_DropsBackloggedJobs pins the documented behavior at pool.go's
// Close(): "Close does not drain the backlog: jobs still queued when Close
// is called are dropped." We submit far more work than the pool can
// possibly process before Close, then assert that the processed-job count
// is strictly less than the submitted count.
func (s *PoolLifecycleTestSuite) TestClose_DropsBackloggedJobs() {
	const n = 50

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		// Long enough that no more than ~one job runs in the gap between
		// Submit and Close.
		time.Sleep(100 * time.Millisecond)
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 2 * time.Second},
			Backlog:      n,
			RateLimit:    1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)

	for range n {
		s.Require().NoError(pool.Submit(new(1)))
	}

	pool.Close()

	// Even after the in-flight handler finishes (Close waits SubmitTimeout
	// for Leave), the dropped backlog jobs are gone forever. Anything else
	// would mean Close drained the backlog, which we explicitly do not.
	s.Require().Less(processed.Load(), int64(n), "Close drained the backlog instead of dropping it")
}
