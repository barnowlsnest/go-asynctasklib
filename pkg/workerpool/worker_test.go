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

	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
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

func (m *mockJobs[T]) Subscribe(w WorkerCloser[T]) error {
	return m.Called(w).Error(0)
}

func (m *mockJobs[T]) Unsubscribe(w WorkerCloser[T]) error {
	return m.Called(w).Error(0)
}

func (m *mockJobs[T]) Claims() chan *Claim[T] {
	return m.Called().Get(0).(chan *Claim[T])
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
		ctx         context.Context
		cfg         *WorkerConfig[string]
		expectedErr error
	}{
		{
			title:       "nil context",
			ctx:         nil,
			cfg:         &WorkerConfig[string]{},
			expectedErr: ErrNilCtx,
		},
		{
			title: "canceled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(s.T().Context())
				cancel()
				return ctx
			}(),
			cfg:         &WorkerConfig[string]{},
			expectedErr: context.Canceled,
		},
		{
			title:       "nil config",
			ctx:         s.T().Context(),
			cfg:         nil,
			expectedErr: ErrInvalidWorker,
		},
		{
			title:       "nil handler",
			ctx:         s.T().Context(),
			cfg:         &WorkerConfig[string]{ID: uint64(1)},
			expectedErr: ErrInvalidWorker,
		},
		{
			title:       "create new worker",
			id:          1,
			ctx:         s.T().Context(),
			cfg:         &WorkerConfig[string]{ID: uint64(1), HandlerFunc: NoopHandler[string]},
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			w, err := NewWorker[string](tc.ctx, tc.cfg)
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
	w, err := NewWorker[string](s.T().Context(), &WorkerConfig[string]{ID: uint64(1), HandlerFunc: NoopHandler[string]})
	s.Require().NoError(err)
	s.Require().NotNil(w)
	s.Require().NotNil(w.events)
	s.Require().IsType(w.events, NewNoopEvents[string]())
}

func (s *WorkerTestSuite) TestNewWorker_CanceledParentContext() {
	parentCtx, parentCancel := context.WithCancel(s.T().Context())
	w := s.newNoopWorker(parentCtx)
	s.Require().NotNil(w)

	var wg sync.WaitGroup
	errs := make(chan error)
	wg.Go(func() {
		<-time.After(100 * time.Millisecond)
		parentCancel()
	})
	wg.Go(func() {
		ctx := w.Context()
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

func (s *WorkerTestSuite) newNoopWorker(ctx context.Context) *Worker[int] {
	s.T().Helper()
	cfg := &WorkerConfig[int]{ID: uint64(1), HandlerFunc: NoopHandler[int]}
	w, err := NewWorker[int](ctx, cfg)
	s.Require().NoError(err)

	return w
}

func (s *WorkerTestSuite) neWorker(ctx context.Context, handler HandlerFunc[int]) *Worker[int] {
	s.T().Helper()
	cfg := &WorkerConfig[int]{ID: uint64(1), HandlerFunc: handler}
	w, err := NewWorker[int](ctx, cfg)
	s.Require().NoError(err)

	return w
}

func (s *WorkerTestSuite) TestJoin_NilJob() {
	w := s.newNoopWorker(s.T().Context())
	s.Require().NotNil(w)
	s.ErrorIs(w.Join(nil), ErrNilJob)
}

func (s *WorkerTestSuite) TestJoin_SubscribeError() {
	w := s.newNoopWorker(s.T().Context())
	s.Require().NotNil(w)

	var errSub = errors.New("failed subscribe")
	jobs := &mockJobs[int]{}
	jobs.On("Subscribe", w).Return(errSub).Once()
	s.Require().ErrorIs(w.Join(jobs), errSub)
}

func (s *WorkerTestSuite) TestWorker_HappyPath() {
	w := s.newNoopWorker(s.T().Context())
	s.Require().NotNil(w)

	// not blocked on writing claims,
	// the next iteration should set started
	claims := make(chan *Claim[int], 1)
	jobs := &mockJobs[int]{}
	jobs.On("Name").Return("test").Once()
	jobs.On("Subscribe", w).Return(nil).Once()
	jobs.On("Claims").Return(claims).Once()

	// should start a worker run loop in a new goroutine
	s.Require().False(w.running.Load())
	s.Require().NoError(w.Join(jobs))
	started := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			<-t.C
			if w.running.Load() {
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
	s.Require().NoError(w.Leave(jobs, time.Second))
	s.Require().False(w.running.Load())
}

func (s *WorkerTestSuite) prepareTestWorker(ctx context.Context, handlerFunc HandlerFunc[int]) *Worker[int] {
	s.T().Helper()
	w := s.neWorker(ctx, handlerFunc)
	s.Require().NotNil(w)

	return w
}

func resend(arrivals chan *int, errs chan error) HandlerFunc[int] {
	return func(_ context.Context, job *int) error {
		if job == nil {
			errs <- errors.New("job is nil")
			return nil
		}

		arrivals <- job
		return nil
	}
}

func (s *WorkerTestSuite) TestWorker_ShouldReceiveJob() {
	ctx := s.T().Context()
	arrivals, errs := make(chan *int), make(chan error)
	w := s.prepareTestWorker(ctx, resend(arrivals, errs))
	jobs, err := NewClaims[int](&ClaimsConfig{})
	s.Require().NoError(err)
	s.Require().NoError(w.Join(jobs))

	job := 1
	var wg sync.WaitGroup
	done := make(chan error)
	wg.Go(func() {
		sub := jobs.Submit(ctx, &job)
		<-sub.Done()
		if err := sub.Err(); err != nil {
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

		v := *val
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
	case <-time.After(time.Second):
		s.FailNow("timeout")
	case err := <-errs:
		s.Require().NoError(err)
	case <-done:
	}
}

func (s *WorkerTestSuite) TestWorker_ShouldNotReceiveJobWhenLeft() {
	ctx := s.T().Context()
	arrivals, errs := make(chan *int), make(chan error)
	w := s.prepareTestWorker(ctx, resend(arrivals, errs))
	cfgChannel := &ClaimsConfig{
		MaxSubmitRetries: 1,
		MaxRetryDelay:    500 * time.Millisecond,
		SubmitTimeout:    500 * time.Millisecond,
	}

	jobs, err := NewClaims[int](cfgChannel)
	s.Require().NoError(err)
	s.Require().NoError(w.Join(jobs))
	s.Require().NoError(w.Leave(jobs, time.Second))
	job1, job2 := 1, 2
	sub1, sub2 := jobs.Submit(ctx, &job1), jobs.Submit(ctx, &job2)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-sub1.Done()
		if err := sub1.Err(); err != nil {
			errs <- err
			return
		}
	})
	wg.Go(func() {
		<-sub2.Done()
		if err := sub2.Err(); err != nil {
			errs <- err
			return
		}
	})

	go func() {
		wg.Wait()
		close(arrivals)
	}()

	select {
	case <-time.After(time.Second):
		s.FailNow("timeout")
	case _, ok := <-arrivals:
		if !ok {
			return
		}
	case err := <-errs:
		s.Require().ErrorContains(err, task.ErrMaxRetriesExceeded.Error())
	}
}
