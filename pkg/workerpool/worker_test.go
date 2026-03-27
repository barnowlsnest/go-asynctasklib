package workerpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type WorkerTestSuite struct {
	suite.Suite
}

func TestWorkerSuite(t *testing.T) {
	suite.Run(t, new(WorkerTestSuite))
}

// fakeWorkable drives worker tests: Subscribe is a no-op success unless subscribeErr is set.
// claimsCh must be buffered so the worker can register at least one claim before tests read it.
type fakeWorkable[T any] struct {
	subscribeErr error
	claimsCh     WorkClaims[T]
	unsubID      uint64
	unsubMu      sync.Mutex
}

func newFakeWorkable[T any](claimsBuf int) *fakeWorkable[T] {
	return &fakeWorkable[T]{claimsCh: make(WorkClaims[T], claimsBuf)}
}

func (f *fakeWorkable[T]) Subscribe(_ *Worker[T]) error {
	return f.subscribeErr
}

func (f *fakeWorkable[T]) Unsubscribe(w *Worker[T]) {
	f.unsubMu.Lock()
	defer f.unsubMu.Unlock()
	f.unsubID = w.ID()
}

func (f *fakeWorkable[T]) Claims() WorkClaims[T] {
	return f.claimsCh
}

func (f *fakeWorkable[T]) lastUnsubID() uint64 {
	f.unsubMu.Lock()
	defer f.unsubMu.Unlock()
	return f.unsubID
}

type recordingEvents[T any] struct {
	mu sync.Mutex

	started   []uint64
	stopped   []uint64
	jobOk     []*T
	jobFailed []struct {
		err error
		job *T
	}
	subscribed      []uint64
	subscribeFailed []struct {
		err error
		id  uint64
	}
	unsubscribed []uint64
}

func (e *recordingEvents[T]) WorkerStarted(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = append(e.started, id)
}

func (e *recordingEvents[T]) WorkerStopped(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = append(e.stopped, id)
}

func (e *recordingEvents[T]) JobOk(job *T) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobOk = append(e.jobOk, job)
}

func (e *recordingEvents[T]) JobFailed(err error, job *T) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobFailed = append(e.jobFailed, struct {
		err error
		job *T
	}{err, job})
}

func (e *recordingEvents[T]) Subscribed(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribed = append(e.subscribed, id)
}

func (e *recordingEvents[T]) SubscribeFailed(err error, id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribeFailed = append(e.subscribeFailed, struct {
		err error
		id  uint64
	}{err, id})
}

func (e *recordingEvents[T]) Unsubscribed(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unsubscribed = append(e.unsubscribed, id)
}

func noopHandlerInt(context.Context, *int) error { return nil }

func (s *WorkerTestSuite) TestNewWorker() {
	cancelledParent := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}()

	tests := []struct {
		name       string
		ctx        context.Context
		cfg        *WorkerConfig[int]
		wantErr    error
		expectedID uint64
	}{
		{
			name:    "nil_config",
			ctx:     context.Background(),
			cfg:     nil,
			wantErr: ErrInvalidWorker,
		},
		{
			name: "nil_handler",
			ctx:  context.Background(),
			cfg: &WorkerConfig[int]{
				ID:          1,
				HandlerFunc: nil,
				Events:      NewNoopEvents[int](),
			},
			wantErr: ErrInvalidWorker,
		},
		{
			name: "canceled_parent_context",
			ctx:  cancelledParent,
			cfg: &WorkerConfig[int]{
				ID:          1,
				HandlerFunc: noopHandlerInt,
				Events:      NewNoopEvents[int](),
			},
			wantErr: context.Canceled,
		},
		{
			name:       "nil_parent_uses_background",
			ctx:        nil,
			expectedID: 99,
			cfg: &WorkerConfig[int]{
				ID:          99,
				HandlerFunc: noopHandlerInt,
				Events:      NewNoopEvents[int](),
			},
		},
		{
			name:       "worker_id_from_config",
			ctx:        context.Background(),
			expectedID: 42,
			cfg: &WorkerConfig[int]{
				ID:          42,
				HandlerFunc: noopHandlerInt,
				Events:      NewNoopEvents[int](),
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			w, err := NewWorker(tc.ctx, tc.cfg)
			if tc.wantErr != nil {
				s.Nil(w)
				s.Require().ErrorIs(err, tc.wantErr)
				return
			}
			s.Require().NoError(err)
			s.Require().NotNil(w)
			s.Require().Equal(tc.expectedID, w.ID())
		})
	}
}

func (s *WorkerTestSuite) TestWorker_Join() {
	s.Run("nil_jobs", func() {
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          1,
			HandlerFunc: noopHandlerInt,
			Events:      NewNoopEvents[int](),
		})
		s.Require().NoError(err)
		s.Require().ErrorIs(w.Join(nil), ErrNilJob)
	})

	s.Run("subscribe_failed", func() {
		ev := &recordingEvents[int]{}
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          7,
			HandlerFunc: noopHandlerInt,
			Events:      ev,
		})
		s.Require().NoError(err)

		subErr := errors.New("subscribe boom")
		fake := &fakeWorkable[int]{subscribeErr: subErr}

		s.Require().ErrorIs(w.Join(fake), subErr)

		ev.mu.Lock()
		defer ev.mu.Unlock()
		s.Require().Len(ev.subscribeFailed, 1)
		s.Equal(subErr, ev.subscribeFailed[0].err)
		s.Equal(uint64(7), ev.subscribeFailed[0].id)
	})

	s.Run("already_joined", func() {
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          1,
			HandlerFunc: noopHandlerInt,
			Events:      NewNoopEvents[int](),
		})
		s.Require().NoError(err)

		fake := newFakeWorkable[int](1)
		go func() { _ = w.Join(fake) }()

		select {
		case <-fake.claimsCh:
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for first claim")
		}

		s.Require().ErrorIs(w.Join(newFakeWorkable[int](1)), ErrWorkerAlreadyJoined)
		s.Require().NoError(w.Shutdown(time.Second))
	})

	handlerErr := errors.New("handler err")
	joinOneJobCases := []struct {
		name    string
		jobVal  int
		handler HandlerFunc[int]
		done    func(ev *recordingEvents[int]) bool
		assert  func(ev *recordingEvents[int])
	}{
		{
			name:   "runs_job_and_lifecycle_events",
			jobVal: 11,
			handler: func(ctx context.Context, job *int) error {
				s.Equal(uint64(3), ctx.Value(CtxWorkerID))
				s.Equal(11, *job)
				return nil
			},
			done: func(ev *recordingEvents[int]) bool {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				return len(ev.jobOk) == 1
			},
			assert: func(ev *recordingEvents[int]) {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				s.Contains(ev.started, uint64(3))
				s.Contains(ev.subscribed, uint64(3))
				s.Len(ev.jobOk, 1)
				s.Equal(11, *ev.jobOk[0])
				s.Contains(ev.stopped, uint64(3))
				s.Contains(ev.unsubscribed, uint64(3))
			},
		},
		{
			name:   "handler_error",
			jobVal: 1,
			handler: func(context.Context, *int) error {
				return handlerErr
			},
			done: func(ev *recordingEvents[int]) bool {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				return len(ev.jobFailed) == 1
			},
			assert: func(ev *recordingEvents[int]) {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				s.Len(ev.jobFailed, 1)
				s.ErrorIs(ev.jobFailed[0].err, handlerErr)
				s.Equal(1, *ev.jobFailed[0].job)
				s.Empty(ev.jobOk)
			},
		},
		{
			name:   "handler_panic",
			jobVal: 1,
			handler: func(context.Context, *int) error {
				panic("boom")
			},
			done: func(ev *recordingEvents[int]) bool {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				return len(ev.jobFailed) == 1
			},
			assert: func(ev *recordingEvents[int]) {
				ev.mu.Lock()
				defer ev.mu.Unlock()
				s.Len(ev.jobFailed, 1)
				s.ErrorIs(ev.jobFailed[0].err, ErrWorkerPanic)
				s.Contains(ev.jobFailed[0].err.Error(), "boom")
			},
		},
	}

	for _, tc := range joinOneJobCases {
		tc := tc
		s.Run(tc.name, func() {
			const workerID uint64 = 3
			ev := &recordingEvents[int]{}
			w, err := NewWorker(context.Background(), &WorkerConfig[int]{
				ID:          workerID,
				HandlerFunc: tc.handler,
				Events:      ev,
			})
			s.Require().NoError(err)

			fake := newFakeWorkable[int](1)
			joinDone := make(chan struct{})
			go func() {
				_ = w.Join(fake)
				close(joinDone)
			}()

			v := tc.jobVal
			select {
			case c := <-fake.claimsCh:
				s.Equal(workerID, c.id)
				c.jobCh <- &v
			case <-time.After(2 * time.Second):
				s.FailNow("timed out waiting for claim")
			}

			s.Assert().Eventually(func() bool {
				return tc.done(ev)
			}, time.Second, 10*time.Millisecond)

			s.Require().NoError(w.Shutdown(time.Second))
			<-joinDone
			tc.assert(ev)
		})
	}
}

func (s *WorkerTestSuite) TestWorker_Shutdown() {
	s.Run("not_started", func() {
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          1,
			HandlerFunc: noopHandlerInt,
			Events:      NewNoopEvents[int](),
		})
		s.Require().NoError(err)
		s.Require().NoError(w.Shutdown(time.Second))
	})

	s.Run("timeout_while_handler_blocks", func() {
		ev := &recordingEvents[int]{}
		block := make(chan struct{})
		handlerEntered := make(chan struct{})

		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID: 1,
			HandlerFunc: func(context.Context, *int) error {
				close(handlerEntered)
				<-block
				return nil
			},
			Events: ev,
		})
		s.Require().NoError(err)

		fake := newFakeWorkable[int](1)
		joinDone := make(chan struct{})
		go func() {
			_ = w.Join(fake)
			close(joinDone)
		}()

		v := 1
		select {
		case c := <-fake.claimsCh:
			c.jobCh <- &v
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for claim")
		}

		<-handlerEntered
		s.Require().ErrorIs(w.Shutdown(50*time.Millisecond), ErrWorkerTimeout)
		close(block)
		select {
		case <-joinDone:
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for Join after unblocking handler")
		}
	})

	s.Run("unsubscribes_on_shutdown", func() {
		ev := &recordingEvents[int]{}
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          9,
			HandlerFunc: noopHandlerInt,
			Events:      ev,
		})
		s.Require().NoError(err)

		fake := newFakeWorkable[int](1)
		go func() { _ = w.Join(fake) }()

		select {
		case <-fake.claimsCh:
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for claim")
		}

		s.Require().NoError(w.Shutdown(time.Second))
		s.Equal(uint64(9), fake.lastUnsubID())

		ev.mu.Lock()
		defer ev.mu.Unlock()
		s.Contains(ev.unsubscribed, uint64(9))
	})

	s.Run("runloop_exits_promptly_after_shutdown", func() {
		w, err := NewWorker(context.Background(), &WorkerConfig[int]{
			ID:          10,
			HandlerFunc: noopHandlerInt,
			Events:      NewNoopEvents[int](),
		})
		s.Require().NoError(err)

		fake := newFakeWorkable[int](1)
		joinDone := make(chan struct{})
		go func() {
			_ = w.Join(fake)
			close(joinDone)
		}()

		select {
		case <-fake.claimsCh:
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for claim")
		}

		s.Require().NoError(w.Shutdown(time.Second))

		select {
		case <-joinDone:
		case <-time.After(100 * time.Millisecond):
			s.FailNow("runLoop did not exit promptly after Shutdown")
		}
	})
}
