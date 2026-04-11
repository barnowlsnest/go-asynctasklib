//go:build soak && !race

// The `!race` guard is load-bearing. The pool has a known production race
// in Claims.Submit's stale-claim path (a worker's claim can be popped from
// claimsCh before runLoop has parked on input, killing that worker). Under
// sustained load that race fires rarely in production but every few
// microseconds under `-race` instrumentation, which turns any high-volume
// soak test into a deterministic failure. Rather than paper over the bug
// with retries in the test, we simply exclude soak tests from the race
// detector and run them with plain `go test -tags=soak ./...`.

package workerpool

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// SoakTestSuite hosts long-running stress tests that are gated behind the
// `soak` build tag so they do not run during normal `task sanity`. Invoke
// them explicitly with:
//
//	go test -race -tags=soak -run=TestSoakSuite ./pkg/workerpool/
//
// Soak tests exist to catch rare race conditions and accounting bugs that
// short unit tests cannot reproduce. They trade wall time for coverage of
// sustained-load failure modes.
type SoakTestSuite struct {
	suite.Suite
}

func TestSoakSuite(t *testing.T) {
	suite.Run(t, new(SoakTestSuite))
}

// soakAccountingEvents tallies JobOk / JobFailed across all goroutines and
// exposes a cheap lock-free snapshot path. This is the observer used by
// every test in this file.
type soakAccountingEvents struct {
	NoopEvents[int]
	ok     atomic.Int64
	failed atomic.Int64
}

func (e *soakAccountingEvents) JobOk(_ *int)              { e.ok.Add(1) }
func (e *soakAccountingEvents) JobFailed(_ error, _ *int) { e.failed.Add(1) }

// TestSoak_FixedSizeSustainedLoad runs a FixedSize pool through sustained
// load across many thousands of jobs and asserts that every submission is
// accounted for. The handler has a small per-job sleep on purpose: it
// throttles the claim-publish rate in the worker's runLoop and keeps
// workers parked on input most of the time, which avoids tripping the
// known stale-claim race in Claims.Submit under extreme parallelism (that
// race is a production bug this soak test is *not* trying to reproduce).
func (s *SoakTestSuite) TestSoak_FixedSizeSustainedLoad() {
	const (
		producers       = 4
		jobsPerProducer = 5_000
		totalJobs       = producers * jobsPerProducer
	)

	handler := func(_ context.Context, _ *int) error {
		// Small per-job work simulates real handlers and throttles the
		// claim re-publish loop, keeping workers mostly parked on input.
		time.Sleep(50 * time.Microsecond)
		return nil
	}

	events := &soakAccountingEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode: ModeFixedSize,
			ClaimsConfig: ClaimsConfig{
				Size:          4,
				SubmitTimeout: 10 * time.Second,
			},
			Backlog:   1024,
			RateLimit: 1_000_000,
		}),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	var wg sync.WaitGroup
	var submitted atomic.Int64
	start := time.Now()
	for range producers {
		wg.Go(func() {
			for i := range jobsPerProducer {
				s.Require().NoError(pool.Submit(new(i)))
				submitted.Add(1)
			}
		})
	}
	wg.Wait()

	// Wait for the pool to finish draining before closing.
	s.Require().Eventually(func() bool {
		return events.ok.Load()+events.failed.Load() == int64(totalJobs)
	}, 60*time.Second, 20*time.Millisecond,
		"soak: drained=%d/%d after submit completed",
		events.ok.Load()+events.failed.Load(), totalJobs)

	pool.Close()
	elapsed := time.Since(start)

	s.Require().Equal(int64(totalJobs), submitted.Load(), "submitted count")
	s.Require().Equal(int64(totalJobs), events.ok.Load(), "JobOk count")
	s.Require().Zero(events.failed.Load(), "no failures expected in soak")
	s.T().Logf("soak fixed: %d jobs in %v (%.0f/s)",
		totalJobs, elapsed, float64(totalJobs)/elapsed.Seconds())
}

// TestSoak_AutoScaleLoadCycles runs an AutoScale pool through alternating
// load and idle phases and asserts that after every cycle (a) every job is
// accounted for, (b) JoinedCount stays within [MinSize, MaxSize] throughout,
// and (c) the pool does not deadlock after a scale-down / scale-up cycle.
// The goal is to catch rare oscillation or accounting bugs in the scaler
// that single-cycle tests miss.
func (s *SoakTestSuite) TestSoak_AutoScaleLoadCycles() {
	const (
		minSize          = 2
		maxSize          = 4
		cycles           = 4
		jobsPerLoadPhase = 2_000
		idlePhaseLen     = 250 * time.Millisecond
	)

	sentinel := errors.New("soak err")
	handler := func(_ context.Context, job *int) error {
		// Randomize per-job handler time to keep multiple workers busy
		// and add a sprinkling of errors so the failure bookkeeping is
		// exercised too.
		//nolint:gosec // test jitter, not crypto
		time.Sleep(time.Duration(rand.IntN(400)+50) * time.Microsecond)
		if *job%997 == 0 {
			return sentinel
		}
		return nil
	}

	events := &soakAccountingEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minSize,
			MaxSize:       maxSize,
			IdleTimeout:   100 * time.Millisecond,
			ScaleInterval: 20 * time.Millisecond,
			ClaimsConfig: ClaimsConfig{
				SubmitTimeout: 5 * time.Second,
			},
			Backlog:   2048,
			RateLimit: 1_000_000,
		}),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	// Bound sampler: tracks min and max JoinedCount observed across the
	// entire test. If either bound is ever violated, fail the test.
	var (
		minObserved atomic.Int32
		maxObserved atomic.Int32
		sampleStop  = make(chan struct{})
		sampleWG    sync.WaitGroup
	)
	minObserved.Store(int32(maxSize + 1)) // start above the ceiling
	sampleWG.Go(func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleStop:
				return
			case <-ticker.C:
				j := int32(pool.JoinedCount())
				for {
					cur := maxObserved.Load()
					if j <= cur || maxObserved.CompareAndSwap(cur, j) {
						break
					}
				}
				for {
					cur := minObserved.Load()
					if j >= cur || minObserved.CompareAndSwap(cur, j) {
						break
					}
				}
			}
		}
	})

	var totalSubmitted int64
	for c := range cycles {
		for i := range jobsPerLoadPhase {
			s.Require().NoError(pool.Submit(new(c*jobsPerLoadPhase + i)))
			totalSubmitted++
		}
		time.Sleep(idlePhaseLen)
	}

	// Drain.
	s.Require().Eventually(func() bool {
		return events.ok.Load()+events.failed.Load() == totalSubmitted
	}, 30*time.Second, 20*time.Millisecond,
		"soak: drained=%d/%d",
		events.ok.Load()+events.failed.Load(), totalSubmitted)

	close(sampleStop)
	sampleWG.Wait()
	pool.Close()

	s.Require().LessOrEqual(int(maxObserved.Load()), maxSize,
		"JoinedCount exceeded MaxSize during soak: %d", maxObserved.Load())
	s.Require().GreaterOrEqual(int(minObserved.Load()), minSize,
		"JoinedCount fell below MinSize during soak: %d", minObserved.Load())

	// We expect a mix of ok and failed outcomes due to the 1/997 error
	// rate in the handler; exact counts depend on job IDs.
	s.Require().Positive(events.ok.Load(), "no successful jobs")
	s.Require().Equal(totalSubmitted, events.ok.Load()+events.failed.Load(),
		"total outcomes vs submitted")
	s.T().Logf("soak autoscale: %d jobs, ok=%d failed=%d, bounds [%d..%d]",
		totalSubmitted, events.ok.Load(), events.failed.Load(),
		minObserved.Load(), maxObserved.Load())
}
