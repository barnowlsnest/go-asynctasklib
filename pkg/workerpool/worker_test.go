package workerpool

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type (
	WorkerTestSuite struct {
		suite.Suite
	}

	mockJobs[T any] struct {
		Jobs[int]
		mock.Mock
	}
)

func (m *mockJobs[T]) Subscribe(w Subscriber[T]) error {
	return m.Called(w).Error(0)
}

func (m *mockJobs[T]) Unsubscribe(w Subscriber[T]) error {
	return m.Called(w).Error(0)
}

func (m *mockJobs[T]) Claims() chan *claim[T] {
	return m.Called().Get(0).(chan *claim[T])
}

func (m *mockJobs[T]) Name() string {
	return m.Called().String(0)
}

func TestWorkerSuite(t *testing.T) {
	suite.Run(t, new(WorkerTestSuite))
}

func (s *WorkerTestSuite) TestNewWorker() {
	testCases := []*struct {
		title       string
		id          uint64
		cfg         *workerConfig[string]
		expectedErr error
	}{
		{
			title:       "nil config",
			cfg:         nil,
			expectedErr: ErrInvalidWorker,
		},
		{
			title:       "nil handler",
			cfg:         &workerConfig[string]{ID: uint64(1)},
			expectedErr: ErrInvalidWorker,
		},
		{
			title:       "create new worker",
			id:          1,
			cfg:         &workerConfig[string]{ID: uint64(1), HandlerFunc: NoopHandler[string]},
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			w, err := newWorker[string](tc.cfg)
			switch tc.expectedErr {
			case nil:
				s.Require().NoError(err)
				s.Require().NotNil(w)
				s.Require().Equal(tc.id, w.ID())
			default:
				s.Require().ErrorIs(err, tc.expectedErr)
				s.Require().Nil(w)
			}
		})
	}
}

func (s *WorkerTestSuite) TestNewWorker_DefaultsEvents() {
	w, err := newWorker[string](&workerConfig[string]{ID: uint64(1), HandlerFunc: NoopHandler[string]})
	s.Require().NoError(err)
	s.Require().NotNil(w)
	s.Require().NotNil(w.events)
	s.Require().IsType(w.events, NewNoopEvents[string]())
}

func (s *WorkerTestSuite) TestNewWorker_ContextBeforeJoinIsNil() {
	w := s.newNoopWorker()
	s.Require().NotNil(w)
	ctx, ok := w.Context()
	s.Require().False(ok)
	s.Require().Nil(ctx)
}

// TestJoin_CanceledParentContext verifies that canceling the parent context
// supplied to Join propagates to the worker's per-Join context.
func (s *WorkerTestSuite) TestJoin_CanceledParentContext() {
	parentCtx, parentCancel := context.WithCancel(s.T().Context())
	w := s.newNoopWorker()
	s.Require().NotNil(w)

	claims, err := newClaims[int](&ClaimsConfig{})
	s.Require().NoError(err)
	s.Require().NoError(w.join(parentCtx, claims))

	var wg sync.WaitGroup
	errs := make(chan error)
	wg.Go(func() {
		<-time.After(100 * time.Millisecond)
		parentCancel()
	})
	wg.Go(func() {
		ctx, ok := w.Context()
		s.Require().True(ok)
		s.Require().NotNil(ctx)
		<-ctx.Done()
		errs <- ctx.Err()
	})

	go func() {
		defer close(errs)
		wg.Wait()
	}()

	select {
	case err := <-errs:
		s.Require().ErrorIs(err, context.Canceled)
	case <-time.After(5 * time.Second):
		s.FailNow("test timeout")
	}
}

func (s *WorkerTestSuite) TestJoin_ContextValidation() {
	s.Run("nil context", func() {
		w := s.newNoopWorker()
		jobs, err := newClaims[int](&ClaimsConfig{})
		s.Require().NoError(err)
		s.Require().ErrorIs(w.join(nil, jobs), ErrNilCtx) //nolint:staticcheck // intentionally passing nil
	})

	s.Run("canceled context", func() {
		w := s.newNoopWorker()
		jobs, err := newClaims[int](&ClaimsConfig{})
		s.Require().NoError(err)
		ctx, cancel := context.WithCancel(s.T().Context())
		cancel()
		s.Require().ErrorIs(w.join(ctx, jobs), context.Canceled)
	})
}

func (s *WorkerTestSuite) newNoopWorker() *worker[int] {
	s.T().Helper()
	cfg := &workerConfig[int]{ID: uint64(1), HandlerFunc: NoopHandler[int]}
	w, err := newWorker[int](cfg)
	s.Require().NoError(err)

	return w
}

func (s *WorkerTestSuite) neWorker(handler HandlerFunc[*int]) *worker[*int] {
	s.T().Helper()
	cfg := &workerConfig[*int]{ID: uint64(1), HandlerFunc: handler}
	w, err := newWorker[*int](cfg)
	s.Require().NoError(err)

	return w
}

func (s *WorkerTestSuite) TestJoin_NilJob() {
	w := s.newNoopWorker()
	s.Require().NotNil(w)
	s.ErrorIs(w.join(s.T().Context(), nil), ErrNilJob)
}

func (s *WorkerTestSuite) TestJoin_SubscribeError() {
	w := s.newNoopWorker()
	s.Require().NotNil(w)

	var errSub = errors.New("failed subscribe")
	jobs := &mockJobs[int]{}
	jobs.On("Subscribe", w).Return(errSub).Once()
	s.Require().ErrorIs(w.join(s.T().Context(), jobs), errSub)
}

func (s *WorkerTestSuite) TestWorker_HappyPath() {
	w := s.newNoopWorker()
	s.Require().NotNil(w)

	// not blocked on writing claims,
	// the next iteration should set started
	claims := make(chan *claim[int], 1)
	jobs := &mockJobs[int]{}
	jobs.On("Name").Return("test").Once()
	jobs.On("Subscribe", w).Return(nil).Once()
	jobs.On("Claims").Return(claims).Once()

	// should start a worker run loop in a new goroutine
	s.Require().False(w.running.Load())
	s.Require().NoError(w.join(s.T().Context(), jobs))
	started := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			<-t.C
			if w.startedNotified.Load() {
				close(started)
				return
			}
		}
	}()

	select {
	case <-time.After(time.Second):
		s.FailNow("not started within timeout")
	case <-started:
		jobs.AssertExpectations(s.T())
	}

	// the worker's run loop goroutine should be blocked from sending claims to the channel until join again
	jobs.On("Unsubscribe", w).Return(nil).Once()
	s.Require().NoError(w.leave(jobs, time.Second))
	s.Require().False(w.running.Load())
}

func (s *WorkerTestSuite) prepareTestWorker(handlerFunc HandlerFunc[*int]) *worker[*int] {
	s.T().Helper()
	w := s.neWorker(handlerFunc)
	s.Require().NotNil(w)

	return w
}

func resend(arrivals chan JobAware[*int], errs chan error) HandlerFunc[*int] {
	return func(ctx JobAware[*int]) error {
		if ctx.Job() == nil {
			errs <- errors.New("job is nil")
			return nil
		}

		arrivals <- ctx
		return nil
	}
}

func (s *WorkerTestSuite) TestWorker_ShouldReceiveJob() {
	ctx := s.T().Context()
	arrivals, errs := make(chan JobAware[*int]), make(chan error)
	w := s.prepareTestWorker(resend(arrivals, errs))
	jobs, err := newClaims[*int](&ClaimsConfig{})
	s.Require().NoError(err)
	s.Require().NoError(w.join(ctx, jobs))

	job := newJobContext[*int](ctx, ctx, new(1))
	var wg sync.WaitGroup
	done := make(chan error)
	wg.Go(func() {
		if err := jobs.submit(job); err != nil {
			errs <- err
			return
		}
	})
	wg.Go(func() {
		val, ok := <-arrivals
		if !ok {
			errs <- errors.New("closed job chan")
			return
		}

		v := *val.Job()
		if v != 1 {
			errs <- errors.New("unexpected value received: " + strconv.FormatInt(int64(v), 10))
			return
		}
	})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-time.After(3 * time.Second):
		s.FailNow("timeout")
	case err := <-errs:
		s.Require().NoError(err)
	case <-done:
	}
}

func (s *WorkerTestSuite) TestWorker_ShouldNotReceiveJobWhenLeft() {
	ctx := s.T().Context()
	// Buffered so the submit goroutines below can deposit their errors
	// even if the assertion loop bails early — without buffering, each
	// extra submitter would leak parked on a chan-send forever.
	arrivals, errs := make(chan JobAware[*int], 2), make(chan error, 2)
	w := s.prepareTestWorker(resend(arrivals, errs))
	cfgChannel := &ClaimsConfig{
		SubmitTimeout: time.Second,
	}

	jobs, err := newClaims[*int](cfgChannel)
	s.Require().NoError(err)
	s.Require().NoError(w.join(ctx, jobs))
	time.Sleep(time.Second)
	s.Require().NoError(w.leave(jobs, time.Second))

	// After Leave there are no subscribers, so each Submit should park for
	// SubmitTimeout and then return ErrNoWorkers. We launch two submitters
	// concurrently and assert *both* see the failure.
	job1, job2 := newJobContext[*int](ctx, ctx, new(1)), newJobContext[*int](ctx, ctx, new(2))
	var wg sync.WaitGroup
	wg.Go(func() {
		errs <- jobs.submit(job1)
	})
	wg.Go(func() {
		errs <- jobs.submit(job2)
	})
	wg.Wait()

	for range 2 {
		select {
		case err := <-errs:
			s.Require().ErrorContains(err, ErrNoWorkers.Error())
		case <-time.After(3 * time.Second):
			s.FailNow("timeout waiting for submit failure")
		}
	}
}

// TestWorker_RejoinAfterLeave verifies that a worker can be Joined again after
// being Left. Each Join builds a fresh per-Join context, so the worker is not
// permanently disabled by an earlier Leave.
func (s *WorkerTestSuite) TestWorker_RejoinAfterLeave() {
	ctx := s.T().Context()
	arrivals, errs := make(chan JobAware[*int], 1), make(chan error, 1)
	w := s.prepareTestWorker(resend(arrivals, errs))

	jobs, err := newClaims[*int](&ClaimsConfig{SubmitTimeout: time.Second})
	s.Require().NoError(err)

	s.Require().NoError(w.join(ctx, jobs))
	time.Sleep(time.Second)
	s.Require().NoError(w.leave(jobs, time.Second))
	s.Require().False(w.running.Load())

	// After a successful Leave, Context() must report no active Join.
	leftCtx, ok := w.Context()
	s.Require().False(ok)
	s.Require().Nil(leftCtx)

	s.Require().NoError(w.join(ctx, jobs))
	wCtx, ok := w.Context()
	s.Require().True(ok)
	s.Require().NotNil(wCtx)
	s.Require().NoError(jobs.submit(newJobContext(wCtx, ctx, new(42))))

	select {
	case got, ok := <-arrivals:
		s.Require().True(ok)
		s.Require().Equal(42, *got.Job())
	case err := <-errs:
		s.Require().NoError(err)
	case <-time.After(10 * time.Second):
		s.FailNow("job was not delivered after rejoin")
	}

	s.Require().NoError(w.leave(jobs, time.Second))
}
