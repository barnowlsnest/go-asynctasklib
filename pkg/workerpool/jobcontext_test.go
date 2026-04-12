package workerpool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type (
	JobContextTestSuite struct {
		suite.Suite
	}

	testContextKey string
)

func TestJobContextSuite(t *testing.T) {
	suite.Run(t, new(JobContextTestSuite))
}

func (s *JobContextTestSuite) TestNewJobContext() {
	t, todo := s.T().Context(), context.TODO()
	ctx := newJobContext[int](t, todo, new(1))
	s.Require().NotNil(ctx)
	s.Require().NotNil(ctx.poolCtx)
	s.Require().NotNil(ctx.submitCtx)
	s.Require().NotNil(ctx.Job())
	s.Require().Equal(1, *ctx.Job())
}

func (s *JobContextTestSuite) TestPoolContextCanceled() {
	pool, cancel := context.WithCancel(s.T().Context())
	ctx := newJobContext[int](pool, context.TODO(), new(1))
	go func() {
		<-time.After(500 * time.Millisecond)
		cancel()
	}()

	select {
	case <-ctx.Done():
		s.Require().ErrorIs(ctx.Err(), context.Canceled)
		s.Require().NoError(ctx.submitCtx.Err())
	case <-time.After(time.Second):
		s.FailNow("context not done within timeout")
	}
}

func (s *JobContextTestSuite) TestSubmitContextCanceled() {
	submit, cancel := context.WithCancel(s.T().Context())
	ctx := newJobContext[int](context.TODO(), submit, new(1))
	go func() {
		<-time.After(100 * time.Millisecond)
		cancel()
	}()

	select {
	case <-ctx.Done():
		s.Require().ErrorIs(ctx.Err(), context.Canceled)
		s.Require().NoError(ctx.poolCtx.Err())
	case <-time.After(time.Second):
		s.FailNow("context not done within timeout")
	}
}

func (s *JobContextTestSuite) TestPoolContextDeadline() {
	pool, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(5*time.Second))
	defer cancel()

	ctx := newJobContext[int](pool, context.TODO(), new(1))
	expected, _ := pool.Deadline()
	actual, ok := ctx.Deadline()
	s.Require().True(ok)
	s.Require().Equal(expected, actual)
}

func (s *JobContextTestSuite) TestSubmitContextDeadline() {
	submit, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(5*time.Second))
	defer cancel()

	ctx := newJobContext[int](context.TODO(), submit, new(1))
	expected, _ := submit.Deadline()
	actual, ok := ctx.Deadline()
	s.Require().True(ok)
	s.Require().Equal(expected, actual)
}

func (s *JobContextTestSuite) TestPoolDeadlineAfterSubmitDeadline() {
	pool, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(30*time.Second))
	defer cancel()

	submit, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(15*time.Second))
	defer cancel()

	ctx := newJobContext[int](pool, submit, new(1))
	expected, _ := submit.Deadline()
	actual, ok := ctx.Deadline()
	s.Require().True(ok)
	s.Require().Equal(expected, actual)
}

func (s *JobContextTestSuite) TestSubmitDeadlineAfterPoolDeadline() {
	pool, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(10*time.Second))
	defer cancel()

	submit, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(20*time.Second))
	defer cancel()

	ctx := newJobContext[int](pool, submit, new(1))
	expected, _ := pool.Deadline()
	actual, ok := ctx.Deadline()
	s.Require().True(ok)
	s.Require().Equal(expected, actual)
}

func (s *JobContextTestSuite) TestNoDeadline() {
	ctx := newJobContext[int](context.TODO(), context.TODO(), new(1))
	actual, ok := ctx.Deadline()
	s.Require().False(ok)
	s.Require().Equal(time.Time{}, actual)
}

func (s *JobContextTestSuite) TestContextValue() {
	var (
		key1 testContextKey = "key1"
		key2 testContextKey = "key2"
	)
	pool := context.WithValue(s.T().Context(), key1, "foo")
	submit := context.WithValue(s.T().Context(), key2, "bar")

	ctx := newJobContext[int](pool, submit, new(1))
	s.Require().Equal("foo", ctx.Value(key1))
	s.Require().Equal("bar", ctx.Value(key2))
}

func (s *JobContextTestSuite) TestContextValueCollision() {
	var pool, submit context.Context
	var (
		id  testContextKey = "id"
		key testContextKey = "key"
	)
	pool = context.WithValue(s.T().Context(), id, "foo")
	submit = context.WithValue(s.T().Context(), id, "bar")
	s.Require().Equal("bar", newJobContext[int](pool, submit, new(1)).Value(id))

	pool = context.WithValue(s.T().Context(), key, "val")
	submit = context.WithValue(s.T().Context(), key, "val")
	s.Require().Equal("val", newJobContext[int](pool, submit, new(1)).Value(key))
}

func (s *JobContextTestSuite) TestPoolContextValue() {
	var pool, submit context.Context
	var id testContextKey = "id"
	pool = context.WithValue(s.T().Context(), id, "bar")
	submit = context.WithValue(s.T().Context(), id, nil)
	s.Require().Equal("bar", newJobContext[int](pool, submit, new(1)).Value(id))
}

func (s *JobContextTestSuite) TestSubmitContextValue() {
	var pool, submit context.Context
	var key testContextKey = "key"
	pool = context.WithValue(s.T().Context(), key, nil)
	submit = context.WithValue(s.T().Context(), key, "foo")
	s.Require().Equal("foo", newJobContext[int](pool, submit, new(1)).Value(key))
}

func (s *JobContextTestSuite) TestNilContextValue() {
	var pool, submit context.Context
	var key testContextKey = "key"
	pool = context.TODO()
	submit = context.TODO()
	s.Require().Nil(newJobContext[int](pool, submit, new(1)).Value(key))
}

func (s *JobContextTestSuite) TestContextTimeout() {
	timeout := time.Second
	pool := s.T().Context()
	submit, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ctx := newJobContext[int](pool, submit, new(1))
	actualDeadline, ok := ctx.Deadline()
	s.Require().True(ok)
	s.Require().Equal(timeout, time.Until(actualDeadline).Round(time.Second))

	select {
	case <-ctx.Done():
		s.ErrorIs(ctx.Err(), context.DeadlineExceeded)
	case <-time.After(timeout + time.Second):
		s.FailNow("context not expired within timeout")
	}
}
