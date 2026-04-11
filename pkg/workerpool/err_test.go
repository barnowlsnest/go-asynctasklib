package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// ErrReportingTestSuite pins the contract of WorkerPool.Err documented in
// pool.go:
//
//	"Err returns the last fatal dispatch error observed by the pool, or nil
//	 if there is none. Transient errors such as ErrSubmitTimeout (in
//	 ModeFixedSize) are swallowed and do not surface here."
//
// The tests below cover the three states that matter: initial (nil),
// happy-path (nil), and transient-drop path (still nil).
type ErrReportingTestSuite struct {
	suite.Suite
}

func TestErrReportingSuite(t *testing.T) {
	suite.Run(t, new(ErrReportingTestSuite))
}

// TestErr_InitiallyNil verifies that a freshly constructed pool reports no
// error — Err() must be safe to call before any Submit and must return nil.
func (s *ErrReportingTestSuite) TestErr_InitiallyNil() {
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
	defer pool.Close()

	s.Require().NoError(pool.Err())
}

// TestErr_NilAfterHappyPath verifies that normal processing (successful
// handler invocations, no drops) leaves Err() nil.
func (s *ErrReportingTestSuite) TestErr_NilAfterHappyPath() {
	const n = 20

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: time.Second},
			Backlog:      n * 2,
			RateLimit:    100000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	for i := range n {
		s.Require().NoError(pool.Submit(new(i)))
	}

	s.Require().Eventually(func() bool {
		return processed.Load() == int64(n)
	}, 3*time.Second, 10*time.Millisecond)

	s.Require().NoError(pool.Err(), "Err() must stay nil on the happy path")
}

// TestErr_NilUnderFixedSizeDispatchDrops verifies the core contract: a
// FixedSize pool that drops jobs via dispatch timeout (because the only
// worker is wedged in a blocking handler) MUST NOT surface those drops via
// Err(). The drops are an expected trade-off of FixedSize mode, not a
// fatal pool condition. If this test ever fails, Err() has become noisy
// and callers using it to decide whether to restart the pool will see
// false positives.
func (s *ErrReportingTestSuite) TestErr_NilUnderFixedSizeDispatchDrops() {
	release := make(chan struct{})
	defer close(release)

	started := make(chan struct{})
	var startedOnce atomic.Bool
	handler := func(_ context.Context, _ *int) error {
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 100 * time.Millisecond},
			Backlog:      8,
			RateLimit:    100000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Wedge the only worker with the seed job.
	s.Require().NoError(pool.Submit(new(1)))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		s.FailNow("seed job did not reach the handler")
	}

	// Every follow-up job will be pulled by listen(), dispatched, the
	// busy worker will not accept, Claims.Submit times out after 100ms,
	// and dispatch() silently drops the job. These are the transient
	// errors the contract says must not surface via Err().
	for i := 2; i <= 6; i++ {
		s.Require().NoError(pool.Submit(new(i)))
	}

	// Wait through several dispatch timeouts (5 * 100ms plus slack).
	time.Sleep(700 * time.Millisecond)

	s.Require().NoError(pool.Err(),
		"Err() must stay nil even when dispatch drops jobs in FixedSize mode")
}
