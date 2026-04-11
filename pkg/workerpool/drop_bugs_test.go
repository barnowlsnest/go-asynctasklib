package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// DropBugsTestSuite pins two production bugs that currently cause submitted
// jobs to vanish without any observable trace:
//
//  1. claims.Submit has a stale-claim race. After popping a Claim from
//     claimsCh it tries `worker.input <- job` in a select with a `default`
//     branch; `default` fires whenever runLoop has not yet entered the
//     inner `case job := <-w.input` line. That gap is a pure scheduler
//     artifact — nanoseconds in normal execution, microseconds to a few
//     milliseconds under -race — but it drops the claim and eventually
//     returns ErrSubmitTimeout even though the worker was alive and about
//     to receive the job.
//
//  2. pool.dispatch silently swallows ErrSubmitTimeout in FixedSize mode:
//     the job is dropped with no Events callback, no pool.Err update, and
//     no log. Combined with #1 this makes bogus drops from the race bug
//     entirely invisible.
//
// These tests MUST FAIL against the current code and PASS after the fix is
// landed. Do not hide them with t.Skip — they are the regression canary.
type DropBugsTestSuite struct {
	suite.Suite
}

func TestDropBugsSuite(t *testing.T) {
	suite.Run(t, new(DropBugsTestSuite))
}

// dropBugsEvents is a tiny Events observer that counts JobOk / JobFailed so
// tests can assert total accounting against the number of submits.
type dropBugsEvents struct {
	NoopEvents[int]
	ok     atomic.Int64
	failed atomic.Int64
}

func (e *dropBugsEvents) JobOk(_ *int)              { e.ok.Add(1) }
func (e *dropBugsEvents) JobFailed(_ error, _ *int) { e.failed.Add(1) }

// TestClaimsSubmit_NoSpuriousTimeouts reveals bug #1 directly against
// Claims.Submit (no WorkerPool wrapper, no dispatch retries). A single
// worker with a fast, never-failing handler is joined to a Claims
// dispatcher; the test hammers Submit in a loop for a bounded duration and
// counts every Submit that returned a non-nil error. For a live,
// subscribed worker the answer must be zero — any non-zero count means
// the stale-claim race dropped a claim for a worker that was about to
// park on its input channel.
//
// The worst form of the bug is a sustained deadlock: once the `default`
// branch eats a claim, the worker remains parked on w.input indefinitely
// (the runLoop only re-publishes a new claim after processing a job, and
// it cannot process anything because its claim is gone). Every subsequent
// Submit then burns a full SubmitTimeout returning ErrSubmitTimeout. The
// bounded-time loop catches both the "occasional false timeout" shape and
// the "wedged forever" shape.
func (s *DropBugsTestSuite) TestClaimsSubmit_NoSpuriousTimeouts() {
	handler := func(_ context.Context, _ *int) error { return nil }
	worker, err := NewWorker(&WorkerConfig[int]{
		ID:          1,
		HandlerFunc: handler,
	})
	s.Require().NoError(err)

	claims, err := NewClaims[int](&ClaimsConfig{
		Size:          1,
		SubmitTimeout: 200 * time.Millisecond,
	})
	s.Require().NoError(err)

	ctx := s.T().Context()
	s.Require().NoError(worker.Join(ctx, claims))
	defer func() { _ = worker.Leave(claims, time.Second) }()

	var submits, timeouts int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		submits++
		v := submits
		if err := claims.Submit(ctx, &v); err != nil {
			timeouts++
		}
	}

	s.Require().Zero(timeouts,
		"claims.Submit returned error on %d/%d submits — stale-claim race dropped live-worker claims",
		timeouts, submits)
}

// TestFixedSizePool_WedgedWorker_DropsAreObservable reveals bug #2. A
// FixedSize=1 pool is wedged in a blocking handler; follow-up submits
// cannot be delivered and will eventually return ErrSubmitTimeout inside
// claims.Submit. The current pool.dispatch path swallows that error and
// returns without touching Events, so the caller has no way to know a job
// was lost. This test asserts that every Submit that returned nil is
// eventually accounted for via Events.JobOk or Events.JobFailed — the
// accounting shortfall is the bug.
//
// defer ordering is load-bearing: close(release) must run before
// pool.Close so the wedged handler returns and the worker goroutine exits
// cleanly (otherwise goleak flags the leak on pool.Close's Leave timeout).
func (s *DropBugsTestSuite) TestFixedSizePool_WedgedWorker_DropsAreObservable() {
	release := make(chan struct{})
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

	events := &dropBugsEvents{}
	pool, err := New(s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 1, SubmitTimeout: 100 * time.Millisecond},
			Backlog:      16,
			RateLimit:    1_000_000,
		}),
		WithHandler(handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)
	defer pool.Close()
	defer close(release)

	// Wedge the single worker with the seed job. The payload is irrelevant
	// (the handler ignores it) — we only need a non-nil *int for Submit.
	s.Require().NoError(pool.Submit(new(int)))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		s.FailNow("seed job did not reach handler")
	}

	// Submit `drops` more jobs. Every one of these is guaranteed to hit
	// ErrSubmitTimeout inside claims.Submit because the only worker is
	// wedged in the blocking handler. With bug #2 present, dispatch()
	// eats those errors silently and failed/ok counts never increment
	// for the dropped jobs.
	const drops = 2
	for range drops {
		s.Require().NoError(pool.Submit(new(int)))
	}

	// Give dispatch enough time to burn through the drops. Each drop runs
	// the dispatch retry loop: maxDispatchRetries+1 Claims.Submit attempts
	// (each capped at SubmitTimeout=100ms) interleaved with
	// fixedSizeRetryWait (100ms) between attempts, so per-drop ~1.1s.
	// 2 drops = ~2.2s, 5s gives comfortable CI slack.
	//
	// We only assert on failed (not ok+failed): the seed job stays wedged
	// in its handler for the whole test, so JobOk for the seed never fires
	// until defer cleanup — it is not part of the drop accounting the bug
	// is about.
	s.Require().Eventually(func() bool {
		return events.failed.Load() >= int64(drops)
	}, 5*time.Second, 20*time.Millisecond,
		"dispatch silently dropped jobs: failed=%d, expected failed>=%d",
		events.failed.Load(), drops)
}
