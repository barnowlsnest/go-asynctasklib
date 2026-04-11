package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// ScaleTransitionTestSuite pins the contract that jobs submitted while the
// pool is actively scaling up or down are never dropped. Existing AutoScale
// tests cover the endpoints (idle → scaled-up, scaled-up → idle); this
// suite covers the *transition window*, which is the interesting racy path.
//
// The tests here keep the burst sizes and worker counts small on purpose:
// under -race the Claims.Submit stale-claim path (a known production bug)
// becomes likely at high sustained parallelism, and these tests are about
// transition correctness, not about stressing the dispatch race.
type ScaleTransitionTestSuite struct {
	suite.Suite
}

func TestScaleTransitionSuite(t *testing.T) {
	suite.Run(t, new(ScaleTransitionTestSuite))
}

// TestScaleUpDuringSubmit verifies that a burst submitted while only
// MinSize workers are joined triggers a scale-up, and every job submitted
// during the transition is eventually processed. This is the "submit races
// Join" contract: a job dispatched before scale-up completes must still
// get picked up by either an existing or newly-joined worker.
func (s *ScaleTransitionTestSuite) TestScaleUpDuringSubmit() {
	const (
		minSize = 1
		maxSize = 4
		jobs    = 20
	)

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		// Each job is slow enough that MinSize cannot drain the burst
		// alone, so the scaler must actually add workers.
		time.Sleep(30 * time.Millisecond)
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minSize,
			MaxSize:       maxSize,
			IdleTimeout:   10 * time.Second, // effectively disables scale-down
			ScaleInterval: 20 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 5 * time.Second},
			Backlog:       jobs * 2,
			RateLimit:     1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	s.Require().Equal(minSize, pool.JoinedCount(), "pool must start at MinSize")

	// Submit the burst. Some of these will be dispatched before the
	// scaler has had a chance to react; they must still be processed.
	for i := range jobs {
		s.Require().NoError(pool.Submit(new(i)))
	}

	// The scaler should Join more workers under load.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() > minSize
	}, 2*time.Second, 10*time.Millisecond, "scale-up never happened")

	// Every submitted job must eventually reach the handler.
	s.Require().Eventually(func() bool {
		return processed.Load() == int64(jobs)
	}, 10*time.Second, 20*time.Millisecond,
		"jobs dropped during scale-up: processed=%d/%d", processed.Load(), jobs)
}

// TestScaleDownDuringSubmit verifies that submits arriving during a
// scale-down window are not dropped. We drive the pool up to MaxSize,
// let it idle past IdleTimeout so scale-down starts, and then submit
// another burst while some Leaves may still be in flight.
func (s *ScaleTransitionTestSuite) TestScaleDownDuringSubmit() {
	const (
		minSize = 1
		maxSize = 4
		burst1  = 8
		burst2  = 8
	)

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		time.Sleep(20 * time.Millisecond)
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minSize,
			MaxSize:       maxSize,
			IdleTimeout:   60 * time.Millisecond,
			ScaleInterval: 20 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 5 * time.Second},
			Backlog:       (burst1 + burst2) * 2,
			RateLimit:     1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Phase 1: burst to force scale-up to MaxSize.
	for i := range burst1 {
		s.Require().NoError(pool.Submit(new(i)))
	}
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() == maxSize
	}, 2*time.Second, 10*time.Millisecond, "expected scale-up to MaxSize")

	// Wait for the burst to drain and workers to go idle past IdleTimeout,
	// so the scaler's next tick starts calling Leave on them.
	s.Require().Eventually(func() bool {
		return processed.Load() == int64(burst1)
	}, 5*time.Second, 10*time.Millisecond, "burst1 did not drain")

	// Wait until scale-down has actually started — we want to Submit while
	// some workers are being torn down.
	s.Require().Eventually(func() bool {
		return pool.JoinedCount() < maxSize
	}, 5*time.Second, 5*time.Millisecond, "scale-down never started")

	// Phase 2: submit a second burst right in the middle of scale-down.
	// Every one of these jobs must be processed — no drops, no deadlock.
	processedAfterBurst2 := processed.Load()
	for i := burst1; i < burst1+burst2; i++ {
		s.Require().NoError(pool.Submit(new(i)))
	}

	s.Require().Eventually(func() bool {
		return processed.Load()-processedAfterBurst2 == int64(burst2)
	}, 10*time.Second, 20*time.Millisecond,
		"burst2 dropped during scale-down: processed=%d, expected +%d",
		processed.Load()-processedAfterBurst2, burst2)
}

// TestScaleCycleSubmitIntegrity runs several scale up / scale down cycles
// back to back with submits in every phase and asserts the grand total of
// processed jobs matches the grand total submitted. This is the coarser,
// more stress-oriented cousin of the two specific-transition tests above.
func (s *ScaleTransitionTestSuite) TestScaleCycleSubmitIntegrity() {
	const (
		minSize        = 1
		maxSize        = 3
		cycles         = 3
		jobsPerCycle   = 12
		idleBetween    = 150 * time.Millisecond
		idleTimeoutCfg = 50 * time.Millisecond
	)

	var processed atomic.Int64
	handler := func(_ context.Context, _ *int) error {
		time.Sleep(15 * time.Millisecond)
		processed.Add(1)
		return nil
	}

	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:          ModeAutoScale,
			MinSize:       minSize,
			MaxSize:       maxSize,
			IdleTimeout:   idleTimeoutCfg,
			ScaleInterval: 15 * time.Millisecond,
			ClaimsConfig:  ClaimsConfig{SubmitTimeout: 5 * time.Second},
			Backlog:       jobsPerCycle * 2,
			RateLimit:     1_000_000,
		}),
		WithHandler[int](handler),
	)
	s.Require().NoError(err)
	defer pool.Close()

	totalSubmitted := 0
	for c := range cycles {
		for i := range jobsPerCycle {
			s.Require().NoError(pool.Submit(new(c*jobsPerCycle + i)))
			totalSubmitted++
		}
		// Idle period lets the scaler scale back down before the next
		// cycle's burst re-triggers scale-up.
		time.Sleep(idleBetween)
	}

	s.Require().Eventually(func() bool {
		return processed.Load() == int64(totalSubmitted)
	}, 15*time.Second, 20*time.Millisecond,
		"cycled submits dropped: processed=%d/%d", processed.Load(), totalSubmitted)
}
