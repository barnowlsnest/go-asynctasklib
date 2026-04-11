package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// HandlerTestSuite covers what the pool does with the *outcome* of a handler:
// success, returned error, panic, and a mixed batch where all three happen
// concurrently. The intent is to pin the contract that the pool surfaces
// every outcome via Events exactly once and that the worker survives every
// failure mode (returned error or panic) without being torn down.
type HandlerTestSuite struct {
	suite.Suite
}

func TestHandlerSuite(t *testing.T) {
	suite.Run(t, new(HandlerTestSuite))
}

// recordingEvents is a NoopEvents specialization that records JobOk and
// JobFailed counts and the unique error chains it has observed. It is safe
// to read from the test goroutine after the pool has stopped processing.
type recordingEvents struct {
	NoopEvents[int]
	mu       sync.Mutex
	ok       int
	failed   int
	failures []error
	jobs     []*int
}

func (c *recordingEvents) JobOk(job *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ok++
	c.jobs = append(c.jobs, job)
}

func (c *recordingEvents) JobFailed(err error, job *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed++
	c.failures = append(c.failures, err)
	c.jobs = append(c.jobs, job)
}

func (c *recordingEvents) snapshot() (ok, failed int, failures []error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ok, c.failed, append([]error(nil), c.failures...)
}

// TestHandlerOutcomes drives a single job through every handler outcome and
// asserts the matching Events.JobOk / Events.JobFailed call fires with the
// right error semantics. Handlers that panic must be wrapped in
// ErrWorkerPanic and the worker must remain subscribed afterwards.
func (s *HandlerTestSuite) TestHandlerOutcomes() {
	sentinel := errors.New("handler boom")

	testCases := []struct {
		title         string
		handler       HandlerFunc[int]
		wantOk        int
		wantFailed    int
		wantErrIsList []error
	}{
		{
			title:      "ok",
			handler:    func(_ context.Context, _ *int) error { return nil },
			wantOk:     1,
			wantFailed: 0,
		},
		{
			title:         "returned error",
			handler:       func(_ context.Context, _ *int) error { return sentinel },
			wantOk:        0,
			wantFailed:    1,
			wantErrIsList: []error{sentinel},
		},
		{
			title:         "panic value",
			handler:       func(_ context.Context, _ *int) error { panic("kaboom") },
			wantOk:        0,
			wantFailed:    1,
			wantErrIsList: []error{ErrWorkerPanic},
		},
		{
			title: "panic error",
			handler: func(_ context.Context, _ *int) error {
				panic(sentinel)
			},
			wantOk:        0,
			wantFailed:    1,
			wantErrIsList: []error{ErrWorkerPanic},
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			events := &recordingEvents{}
			pool, err := New[int](s.T().Context(),
				WithConfig[int](&Config{
					// Size=4 gives the dispatcher several claims to pick
					// from, so the stale-claim race in Claims.Submit (where
					// a worker's claim is popped before its runLoop has
					// parked on input) can't silently eat the single job
					// we submit — at least one of the four workers will be
					// ready by the time dispatch runs.
					Mode:         ModeFixedSize,
					ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: time.Second},
					Backlog:      4,
					RateLimit:    100000,
				}),
				WithHandler[int](tc.handler),
				WithEvents[int](events),
			)
			s.Require().NoError(err)
			defer pool.Close()

			s.Require().NoError(pool.Submit(new(1)))

			s.Require().Eventually(func() bool {
				ok, failed, _ := events.snapshot()
				return ok == tc.wantOk && failed == tc.wantFailed
			}, 2*time.Second, 5*time.Millisecond)

			_, _, failures := events.snapshot()
			for _, want := range tc.wantErrIsList {
				found := false
				for _, got := range failures {
					if errors.Is(got, want) {
						found = true
						break
					}
				}
				s.Require().True(found, "expected failure containing %v, got %v", want, failures)
			}

			// The worker must still be alive and willing to process more
			// jobs after a returned error or a panic — that is the whole
			// point of recovering inside processJob. The follow-up Submit
			// goes through the same tc.handler, so we just assert the
			// total event count doubles.
			s.Require().NoError(pool.Submit(new(2)))
			s.Require().Eventually(func() bool {
				ok, failed, _ := events.snapshot()
				return ok+failed == 2
			}, 2*time.Second, 5*time.Millisecond, "worker did not process the follow-up job")
		})
	}
}

// TestHandlerMixedBatch dispatches a large batch through one pool with a
// handler that returns ok/error/panic based on the input value, and asserts
// the final tally on the events observer matches exactly. This catches the
// kind of "off by one in the failure path" bugs that single-job tests miss.
func (s *HandlerTestSuite) TestHandlerMixedBatch() {
	const (
		nOk     = 60
		nErr    = 25
		nPanic  = 15
		nTotal  = nOk + nErr + nPanic
		errMark = 2
		panMark = 3
	)

	sentinel := errors.New("mixed batch err")

	handler := func(_ context.Context, job *int) error {
		switch *job {
		case errMark:
			return sentinel
		case panMark:
			panic("mixed batch panic")
		default:
			return nil
		}
	}

	events := &recordingEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: 2 * time.Second},
			Backlog:      nTotal,
			RateLimit:    100000,
		}),
		WithHandler[int](handler),
		WithEvents[int](events),
	)
	s.Require().NoError(err)
	defer pool.Close()

	// Submit oks first, then errs, then panics — order is irrelevant for
	// the assertion (we count totals only) but the spread keeps any single
	// worker from accidentally only seeing one outcome class.
	for range nOk {
		s.Require().NoError(pool.Submit(new(1)))
	}

	for range nErr {
		s.Require().NoError(pool.Submit(new(errMark)))
	}

	for range nPanic {
		s.Require().NoError(pool.Submit(new(panMark)))
	}

	s.Require().Eventually(func() bool {
		ok, failed, _ := events.snapshot()
		return ok+failed == nTotal
	}, 5*time.Second, 10*time.Millisecond)

	ok, failed, failures := events.snapshot()
	s.Require().Equal(nOk, ok, "ok count")
	s.Require().Equal(nErr+nPanic, failed, "failed count (errors + panics)")

	var errCount, panicCount int
	for _, f := range failures {
		switch {
		case errors.Is(f, ErrWorkerPanic):
			panicCount++
		case errors.Is(f, sentinel):
			errCount++
		}
	}

	s.Require().Equal(nErr, errCount, "returned-error failures")
	s.Require().Equal(nPanic, panicCount, "recovered-panic failures")
}
