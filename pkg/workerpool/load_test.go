//go:build load

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

// LoadTestSuite hosts long-running stress tests that are gated behind the
// `load` build tag so they do not run during normal `task sanity`. Invoke
// them explicitly with:
//
//	go test -race -tags=load -run=TestLoadSuite ./pkg/workerpool/
//
// Load tests exist to catch rare race conditions and accounting bugs that
// short unit tests cannot reproduce. They trade wall time for coverage of
// sustained-load failure modes.
type LoadTestSuite struct {
	suite.Suite
}

func TestLoadSuite(t *testing.T) {
	suite.Run(t, new(LoadTestSuite))
}

// loadAccountingEvents tallies JobOk / JobFailed across all goroutines and
// exposes a cheap lock-free snapshot path. This is the observer used by
// every test in this file.
type loadAccountingEvents struct {
	NoopEvents[int]
	ok     atomic.Int64
	failed atomic.Int64
}

func (e *loadAccountingEvents) JobOk(_ *int)              { e.ok.Add(1) }
func (e *loadAccountingEvents) JobFailed(_ error, _ *int) { e.failed.Add(1) }

// TestLoad_FixedSizeSustainedLoad runs a FixedSize pool through sustained
// load across many thousands of jobs and asserts that every submission is
// accounted for.
func (s *LoadTestSuite) TestLoad_FixedSizeSustainedLoad() {
	const (
		producers       = 4
		jobsPerProducer = 5_000
		totalJobs       = producers * jobsPerProducer
	)

	handler := func(_ context.Context, _ *int) error {
		return nil
	}

	events := &loadAccountingEvents{}
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
		"load: drained=%d/%d after submit completed",
		events.ok.Load()+events.failed.Load(), totalJobs)

	pool.Close()
	elapsed := time.Since(start)

	s.Require().Equal(int64(totalJobs), submitted.Load(), "submitted count")
	s.Require().Equal(int64(totalJobs), events.ok.Load(), "JobOk count")
	s.Require().Zero(events.failed.Load(), "no failures expected in load")
	s.T().Logf("load fixed: %d jobs in %v (%.0f/s)",
		totalJobs, elapsed, float64(totalJobs)/elapsed.Seconds())
}

// TestLoad_AutoScaleLoadCycles runs an AutoScale pool through alternating
// load and idle phases and asserts that after every cycle (a) every job is
// accounted for, (b) JoinedCount stays within [MinSize, MaxSize] throughout,
// and (c) the pool does not deadlock after a scale-down / scale-up cycle.
// The goal is to catch rare oscillation or accounting bugs in the scaler
// that single-cycle tests miss.
func (s *LoadTestSuite) TestLoad_AutoScaleLoadCycles() {
	const (
		minSize          = 2
		maxSize          = 4
		cycles           = 4
		jobsPerLoadPhase = 2_000
		idlePhaseLen     = 250 * time.Millisecond
	)

	sentinel := errors.New("load err")
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

	events := &loadAccountingEvents{}
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
		"load: drained=%d/%d",
		events.ok.Load()+events.failed.Load(), totalSubmitted)

	close(sampleStop)
	sampleWG.Wait()
	pool.Close()

	s.Require().LessOrEqual(int(maxObserved.Load()), maxSize,
		"JoinedCount exceeded MaxSize during load: %d", maxObserved.Load())
	s.Require().GreaterOrEqual(int(minObserved.Load()), minSize,
		"JoinedCount fell below MinSize during load: %d", minObserved.Load())

	// We expect a mix of ok and failed outcomes due to the 1/997 error
	// rate in the handler; exact counts depend on job IDs.
	s.Require().Positive(events.ok.Load(), "no successful jobs")
	s.Require().Equal(totalSubmitted, events.ok.Load()+events.failed.Load(),
		"total outcomes vs submitted")
	s.T().Logf("load autoscale: %d jobs, ok=%d failed=%d, bounds [%d..%d]",
		totalSubmitted, events.ok.Load(), events.failed.Load(),
		minObserved.Load(), maxObserved.Load())
}
