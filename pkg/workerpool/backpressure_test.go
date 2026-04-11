package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// BackpressureTestSuite pins three related contracts that the pool must
// honor under pressure:
//
//  1. RateLimit is actually enforced on the happy path — submitting N jobs
//     at a RateLimit of R tokens/sec takes at least ~(N-burst)/R wall time.
//  2. When the backlog is saturated, SubmitContext is observably bounded by
//     the caller's context — callers can escape via ctx cancelation rather
//     than being parked forever.
//  3. FixedSize mode surfaces dispatch-timeout drops via Events.JobFailed so
//     callers can observe every lost job. Err() stays nil because transient
//     ErrSubmitTimeout is not a fatal dispatcher state — that contract is
//     preserved — but the drops themselves are no longer silent.
type BackpressureTestSuite struct {
	suite.Suite
}

func TestBackpressureSuite(t *testing.T) {
	suite.Run(t, new(BackpressureTestSuite))
}

// TestRateLimit_RespectsRate submits N jobs through a pool with RateLimit R
// and burst equal to Size=1, and asserts that the wall-clock duration of the
// submit loop is at least (N-1)/R — i.e. the rate limiter actually parks the
// producer between tokens. If the limiter were bypassed the loop would
// finish in single-digit milliseconds, well under the assertion.
func (s *BackpressureTestSuite) TestRateLimit_RespectsRate() {
	const (
		n           = 10
		rateLimit   = 50.0
		minDuration = 150 * time.Millisecond // (n-1)/rateLimit = 180ms, slack for CI jitter
	)

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 5 * time.Second},
			Backlog:      n * 2,
			RateLimit:    rateLimit,
		}),
		WithHandler[int](NoopHandler[int]),
	)
	s.Require().NoError(err)
	defer pool.Close()

	start := time.Now()
	for range n {
		s.Require().NoError(pool.Submit(new(1)))
	}
	elapsed := time.Since(start)

	s.Require().GreaterOrEqual(elapsed, minDuration,
		"rate limit not enforced: %d submits at %.0f rps took %v, expected >= %v",
		n, rateLimit, elapsed, minDuration)
}

// TestSubmitContext_BacklogFullHonorsCtxDeadline pins the caller-side escape
// hatch for backpressure: when the backlog is full and listen() is stuck in
// dispatch (because the only worker is blocked in a long-running handler),
// SubmitContext with a short ctx deadline must return the caller's error
// within that deadline — not block on SubmitTimeout, and not park forever.
func (s *BackpressureTestSuite) TestSubmitContext_BacklogFullHonorsCtxDeadline() {
	release := make(chan struct{})
	defer close(release)

	started := make(chan struct{})
	var startedOnce atomic.Bool
	handler := func(_ context.Context, _ *int) error {
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		<-release
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode: ModeFixedSize,
			// SubmitTimeout is long enough that if the test accidentally
			// exercises the pool's backlog timeout instead of the caller's
			// ctx deadline, the assertion below fails loudly on duration.
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 10 * time.Second},
			Backlog:      1,
			RateLimit:    1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Seed job: pulled from backlog, dispatched, worker starts blocking.
	s.Require().NoError(pool.Submit(new(1)))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		s.FailNow("seed job did not reach the handler — dispatch race?")
	}

	// Second job: goes into backlog, listen() pulls it, dispatch parks on
	// Claims.Submit waiting for the stuck worker (up to SubmitTimeout=10s).
	// listen() is now frozen for the rest of the test.
	s.Require().NoError(pool.Submit(new(2)))

	// Wait for listen() to actually pick up job #2 and enter dispatch. A
	// short sleep is enough because listen() wakes within microseconds of
	// a channel send.
	time.Sleep(20 * time.Millisecond)

	// Fill the one backlog slot that is now free (listen already pulled the
	// previous occupant). After this, backlog is full and nobody is reading.
	s.Require().NoError(pool.Submit(new(3)))

	// Now the pool is fully backpressured: worker blocked, listen blocked,
	// backlog full. SubmitContext with a 75ms ctx deadline must return
	// within roughly that deadline with the caller's error.
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = pool.SubmitContext(ctx, new(4))
	elapsed := time.Since(start)

	s.Require().ErrorIs(err, context.DeadlineExceeded,
		"expected caller ctx deadline error, got %v", err)
	s.Require().Less(elapsed, 500*time.Millisecond,
		"SubmitContext waited past the caller deadline: %v", elapsed)
}

// dropTrackingEvents records every JobOk / JobFailed call so we can assert
// that dropped-at-dispatch jobs emit nothing.
type dropTrackingEvents struct {
	NoopEvents[int]
	ok     atomic.Int32
	failed atomic.Int32
}

func (d *dropTrackingEvents) JobOk(_ *int)              { d.ok.Add(1) }
func (d *dropTrackingEvents) JobFailed(_ error, _ *int) { d.failed.Add(1) }

// TestFixedSize_DropsOnDispatchTimeoutAreObservable is the contract test
// for pool.go's dispatch() drop path. A single worker is wedged in a
// blocking handler; follow-up jobs are pulled by listen(), dispatched,
// retried up to maxDispatchRetries, and finally dropped. Every dropped
// job must fire Events.JobFailed so callers have an observability hook.
// Err() stays nil because ErrSubmitTimeout is a transient dispatch error,
// not a fatal dispatcher state, and the Err() contract remains reserved
// for fatals like ErrDispatcherClosed.
func (s *BackpressureTestSuite) TestFixedSize_DropsOnDispatchTimeoutAreObservable() {
	release := make(chan struct{})
	defer close(release)

	started := make(chan struct{})
	var startedOnce atomic.Bool
	handler := func(_ context.Context, _ *int) error {
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		// Long fallback so the handler stays wedged for the entire test
		// window. The handler only exits via defer close(release).
		select {
		case <-release:
		case <-time.After(30 * time.Second):
		}
		return nil
	}

	events := &dropTrackingEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 100 * time.Millisecond},
			Backlog:      8,
			RateLimit:    1_000_000,
		}),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Seed job wedges the worker. The payload is irrelevant (the handler
	// ignores it) — we only need a non-nil *int for Submit.
	s.Require().NoError(pool.Submit(new(int)))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		s.FailNow("seed job did not reach the handler — dispatch race?")
	}

	// `drops` follow-up jobs. listen() pulls each in turn and calls
	// dispatch(), which runs the retry loop (maxDispatchRetries+1
	// Claims.Submit attempts interleaved with fixedSizeRetryWait) then
	// fires JobFailed for each. Per-drop cost ~1.1s, so 2 serialized
	// drops need ~2.2s; 5s gives slack.
	const drops = 2
	for range drops {
		s.Require().NoError(pool.Submit(new(int)))
	}

	s.Require().Eventually(func() bool {
		return events.failed.Load() >= int32(drops)
	}, 5*time.Second, 20*time.Millisecond,
		"dispatch did not surface drops via JobFailed: failed=%d, expected >=%d",
		events.failed.Load(), drops)

	// The seed is still wedged in its handler, so JobOk must stay at zero.
	s.Require().Zero(events.ok.Load(),
		"worker is still blocked — no JobOk should have fired yet")
	// Err() stays reserved for fatal dispatcher state; transient
	// ErrSubmitTimeout drops must not poison it.
	s.Require().NoError(pool.Err(),
		"ErrSubmitTimeout is not a fatal pool error in FixedSize mode")
}
