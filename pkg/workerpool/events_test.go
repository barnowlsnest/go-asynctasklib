package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// EventsTestSuite pins the contracts that the Events observer must honor,
// independent of what any individual handler does:
//
//  1. Per-job outcome parity — every job that reaches the handler triggers
//     exactly one of JobOk / JobFailed, never both and never neither.
//  2. Per-worker lifecycle ordering — for any worker ID, Subscribed happens
//     before WorkerStarted (the runLoop goroutine can only publish a claim
//     after the pool registered it), and Unsubscribed happens before
//     WorkerStopped (the runLoop only exits after its per-Join context was
//     canceled by Leave, which is called after Unsubscribe).
//  3. Lifecycle completeness — by the time Close returns, every Subscribed
//     worker has been Unsubscribed, and every WorkerStarted has been
//     WorkerStopped. No dangling lifecycle events.
type EventsTestSuite struct {
	suite.Suite
}

func TestEventsSuite(t *testing.T) {
	suite.Run(t, new(EventsTestSuite))
}

// eventKind identifies which observer method fired.
type eventKind int

const (
	kindSubscribed eventKind = iota
	kindWorkerStarted
	kindUnsubscribed
	kindWorkerStopped
	kindJobOk
	kindJobFailed
)

type eventRecord struct {
	kind     eventKind
	workerID uint64
	job      *int
	err      error
	seq      int // monotonic insertion order
}

// sequencedEvents is an Events implementation that captures every call
// with an insertion sequence number so tests can assert per-worker
// ordering without relying on wall-clock timestamps.
type sequencedEvents struct {
	mu      sync.Mutex
	events  []eventRecord
	nextSeq int
}

func (e *sequencedEvents) record(r eventRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r.seq = e.nextSeq
	e.nextSeq++
	e.events = append(e.events, r)
}

func (e *sequencedEvents) JobOk(job *int) {
	e.record(eventRecord{kind: kindJobOk, job: job})
}

func (e *sequencedEvents) JobFailed(err error, job *int) {
	e.record(eventRecord{kind: kindJobFailed, err: err, job: job})
}

func (e *sequencedEvents) Subscribed(id uint64) {
	e.record(eventRecord{kind: kindSubscribed, workerID: id})
}

func (e *sequencedEvents) Unsubscribed(id uint64) {
	e.record(eventRecord{kind: kindUnsubscribed, workerID: id})
}

func (e *sequencedEvents) WorkerStarted(id uint64) {
	e.record(eventRecord{kind: kindWorkerStarted, workerID: id})
}

func (e *sequencedEvents) WorkerStopped(id uint64) {
	e.record(eventRecord{kind: kindWorkerStopped, workerID: id})
}

func (e *sequencedEvents) SubscribeFailed(_ error, _ uint64)      {}
func (e *sequencedEvents) UnsubscribeFailed(_ error, _ uint64)    {}
func (e *sequencedEvents) LeaveTimeout(_ uint64, _ time.Duration) {}

// snapshot returns a copy of the event log. Tests should call this after
// the pool has been closed; calling it while the pool is still live is
// racy (the worker goroutines may still be appending).
func (e *sequencedEvents) snapshot() []eventRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]eventRecord, len(e.events))
	copy(out, e.events)
	return out
}

// TestJobOutcomeParity submits a mixed batch through a multi-worker pool
// and asserts that the total number of JobOk + JobFailed events exactly
// matches the number of jobs that actually reached the handler. This is
// the "exactly-once" delivery contract from the handler's perspective.
func (s *EventsTestSuite) TestJobOutcomeParity() {
	const (
		okMark    = 1
		errMark   = 2
		panicMark = 3
		nOk       = 40
		nErr      = 20
		nPanic    = 10
		nTotal    = nOk + nErr + nPanic
	)

	sentinel := errors.New("parity test err")
	handler := func(_ context.Context, job *int) error {
		switch *job {
		case errMark:
			return sentinel
		case panicMark:
			panic("parity test panic")
		default:
			return nil
		}
	}

	events := &sequencedEvents{}
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

	for range nOk {
		s.Require().NoError(pool.Submit(new(okMark)))
	}
	for range nErr {
		s.Require().NoError(pool.Submit(new(errMark)))
	}
	for range nPanic {
		s.Require().NoError(pool.Submit(new(panicMark)))
	}

	// Wait for every job to produce an outcome before closing — otherwise
	// Close's drop-the-backlog semantics would eat the tail and distort the
	// count.
	s.Require().Eventually(func() bool {
		e := events.snapshot()
		count := 0
		for _, r := range e {
			if r.kind == kindJobOk || r.kind == kindJobFailed {
				count++
			}
		}
		return count == nTotal
	}, 5*time.Second, 10*time.Millisecond, "did not observe nTotal outcome events")

	pool.Close()

	snap := events.snapshot()
	var ok, failed int
	for _, r := range snap {
		switch r.kind {
		case kindJobOk:
			ok++
		case kindJobFailed:
			failed++
		}
	}

	s.Require().Equal(nOk, ok, "JobOk count")
	s.Require().Equal(nErr+nPanic, failed, "JobFailed count")
	s.Require().Equal(nTotal, ok+failed, "total outcome events")
}

// TestWorkerLifecycleOrder asserts the per-worker event ordering invariants
// on a single-worker pool. The assertions are symbolic — we only look at
// sequence numbers, never wall-clock — so the test cannot flake on slow CI.
func (s *EventsTestSuite) TestWorkerLifecycleOrder() {
	events := &sequencedEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: 4, SubmitTimeout: time.Second},
			Backlog:      4,
			RateLimit:    100000,
		}),
		WithHandler[int](NoopHandler[int]),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	// Submit a job so WorkerStarted is guaranteed to have fired: it is
	// emitted the first time runLoop successfully publishes its claim.
	// With Size=4 at least one worker will reach that point quickly.
	s.Require().NoError(pool.Submit(new(1)))

	// Wait until we have at least one JobOk so we know at least one worker
	// progressed far enough to emit WorkerStarted on the happy path.
	s.Require().Eventually(func() bool {
		for _, r := range events.snapshot() {
			if r.kind == kindJobOk {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	pool.Close()

	snap := events.snapshot()

	// Group events by worker ID. For each worker, compute the first seq of
	// each lifecycle kind we care about (zero value means "never seen", so
	// we use a pointer to distinguish "never" from "seen at seq 0").
	type lifecycleSeqs struct {
		subscribed    *int
		workerStarted *int
		unsubscribed  *int
		workerStopped *int
	}
	byWorker := make(map[uint64]*lifecycleSeqs)

	for _, r := range snap {
		if r.workerID == 0 {
			continue // job events carry no worker id
		}
		l, ok := byWorker[r.workerID]
		if !ok {
			l = &lifecycleSeqs{}
			byWorker[r.workerID] = l
		}
		seq := r.seq
		switch r.kind {
		case kindSubscribed:
			if l.subscribed == nil {
				l.subscribed = &seq
			}
		case kindWorkerStarted:
			if l.workerStarted == nil {
				l.workerStarted = &seq
			}
		case kindUnsubscribed:
			if l.unsubscribed == nil {
				l.unsubscribed = &seq
			}
		case kindWorkerStopped:
			if l.workerStopped == nil {
				l.workerStopped = &seq
			}
		}
	}

	s.Require().NotEmpty(byWorker, "no worker lifecycle events captured")

	for id, l := range byWorker {
		// Every worker that was seen at all must have been Subscribed and
		// Unsubscribed by the time Close returned. WorkerStarted is not
		// guaranteed for every worker (a worker whose runLoop never got
		// CPU time before Close will skip it), so we only assert the order
		// if it was observed.
		s.Require().NotNil(l.subscribed, "worker %d: no Subscribed event", id)
		s.Require().NotNil(l.unsubscribed, "worker %d: no Unsubscribed event", id)
		s.Require().NotNil(l.workerStopped, "worker %d: no WorkerStopped event", id)

		s.Require().Less(*l.subscribed, *l.unsubscribed,
			"worker %d: Subscribed(seq=%d) must precede Unsubscribed(seq=%d)",
			id, *l.subscribed, *l.unsubscribed)

		s.Require().Less(*l.unsubscribed, *l.workerStopped,
			"worker %d: Unsubscribed(seq=%d) must precede WorkerStopped(seq=%d)",
			id, *l.unsubscribed, *l.workerStopped)

		if l.workerStarted != nil {
			s.Require().Less(*l.subscribed, *l.workerStarted,
				"worker %d: Subscribed(seq=%d) must precede WorkerStarted(seq=%d)",
				id, *l.subscribed, *l.workerStarted)
			s.Require().Less(*l.workerStarted, *l.workerStopped,
				"worker %d: WorkerStarted(seq=%d) must precede WorkerStopped(seq=%d)",
				id, *l.workerStarted, *l.workerStopped)
		}
	}
}

// TestLifecycleCompletenessOnClose verifies that by the time Close returns,
// every worker that was Subscribed during the pool's lifetime has also been
// Unsubscribed and WorkerStopped. This is the "no dangling lifecycle events"
// contract — the thing that catches resource leaks in observability plumbing.
func (s *EventsTestSuite) TestLifecycleCompletenessOnClose() {
	const size = 6

	events := &sequencedEvents{}
	pool, err := New[int](s.T().Context(),
		WithConfig[int](&Config{
			Mode:         ModeFixedSize,
			ClaimsConfig: ClaimsConfig{Size: size, SubmitTimeout: time.Second},
			Backlog:      8,
			RateLimit:    100000,
		}),
		WithHandler[int](NoopHandler[int]),
		WithEvents[int](events),
	)
	s.Require().NoError(err)

	pool.Close()

	snap := events.snapshot()
	subscribed := make(map[uint64]struct{})
	unsubscribed := make(map[uint64]struct{})
	stopped := make(map[uint64]struct{})
	for _, r := range snap {
		switch r.kind {
		case kindSubscribed:
			subscribed[r.workerID] = struct{}{}
		case kindUnsubscribed:
			unsubscribed[r.workerID] = struct{}{}
		case kindWorkerStopped:
			stopped[r.workerID] = struct{}{}
		}
	}

	s.Require().Len(subscribed, size, "expected Subscribed event for every worker")

	for id := range subscribed {
		_, uok := unsubscribed[id]
		s.Require().True(uok, "worker %d was Subscribed but never Unsubscribed", id)
		_, sok := stopped[id]
		s.Require().True(sok, "worker %d was Subscribed but never WorkerStopped", id)
	}
}
