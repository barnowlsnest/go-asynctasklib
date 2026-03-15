package yielder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type YielderSuite struct {
	suite.Suite
}

func TestYielderSuite(t *testing.T) {
	suite.Run(t, new(YielderSuite))
}

// --- Constructor validation ---

func (s *YielderSuite) TestNewReturnsErrorOnCancelledContext() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	y, err := New[int](ctx, WithValues(ptrs(1)))
	s.Nil(y)
	s.ErrorIs(err, context.Canceled)
}

func (s *YielderSuite) TestNewReturnsErrorWhenFnIsNil() {
	y, err := New[int](context.Background())
	s.Nil(y)
	s.ErrorIs(err, ErrNil)
}

func (s *YielderSuite) TestNewAppliesDefaultTimeout() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)))
	s.NoError(err)
	defer y.Stop()
	s.Equal(defaultTimeout, y.timeout)
}

func (s *YielderSuite) TestNewAppliesDefaultBufOnNegative() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)), WithBuffer[int](-5))
	s.NoError(err)
	defer y.Stop()
	s.Equal(defaultBuf, y.buf)
}

func (s *YielderSuite) TestNewRespectsCustomTimeout() {
	y, err := New[int](context.Background(),
		WithValues(ptrs(1)),
		WithTimeout[int](5*time.Second),
	)
	s.NoError(err)
	defer y.Stop()
	s.Equal(5*time.Second, y.timeout)
}

func (s *YielderSuite) TestNewRespectsCustomBuffer() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)), WithBuffer[int](10))
	s.NoError(err)
	defer y.Stop()
	s.Equal(10, y.buf)
}

// --- WithValues ---

func (s *YielderSuite) TestWithValuesEmitsAllValues() {
	vals := ptrs(1, 2, 3, 4, 5)
	y, err := New[int](context.Background(), WithValues(vals))
	s.NoError(err)

	var got []int
	for v := range y.Results() {
		got = append(got, *v)
	}
	s.Equal([]int{1, 2, 3, 4, 5}, got)
	s.NoError(y.Err())
}

func (s *YielderSuite) TestWithValuesEmptySlice() {
	y, err := New[int](context.Background(), WithValues[int](nil))
	s.NoError(err)

	var got []int
	for v := range y.Results() {
		got = append(got, *v)
	}
	s.Empty(got)
	s.NoError(y.Err())
}

// --- WithGeneratorFunc ---

func (s *YielderSuite) TestWithGeneratorFuncEmitsValues() {
	y, err := New[string](context.Background(),
		WithGeneratorFunc(func() ([]*string, error) {
			a, b := "hello", "world"
			return []*string{&a, &b}, nil
		}),
	)
	s.NoError(err)

	var got []string
	for v := range y.Results() {
		got = append(got, *v)
	}
	s.Equal([]string{"hello", "world"}, got)
	s.NoError(y.Err())
}

func (s *YielderSuite) TestGeneratorFuncErrorStopsYielder() {
	genErr := errors.New("generator failed")
	y, err := New[int](context.Background(),
		WithGeneratorFunc(func() ([]*int, error) {
			return nil, genErr
		}),
	)
	s.NoError(err)

	<-y.Done()
	for range y.Results() {
		s.Fail("should not emit values on error")
	}
	s.ErrorIs(y.Err(), genErr)
}

// --- Panic recovery ---

func (s *YielderSuite) TestPanicInGeneratorIsRecovered() {
	y, err := New[int](context.Background(),
		WithGeneratorFunc(func() ([]*int, error) {
			panic("boom")
		}),
	)
	s.NoError(err)

	<-y.Done()
	for range y.Results() {
		s.Fail("should not emit values on panic")
	}
	s.Error(y.Err())
	s.Contains(y.Err().Error(), "boom")
}

// --- Stop ---

func (s *YielderSuite) TestStopClosesResultsChannel() {
	y, err := New[int](context.Background(),
		WithValues(ptrs(1, 2, 3)),
		WithBuffer[int](0),
		WithTimeout[int](10*time.Second),
	)
	s.NoError(err)

	y.Stop()
	<-y.Done()

	var got []int
	for v := range y.Results() {
		got = append(got, *v)
	}
	// may have emitted some or none before stop
	s.LessOrEqual(len(got), 3)
}

func (s *YielderSuite) TestStopIsIdempotent() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)))
	s.NoError(err)

	s.NotPanics(func() {
		y.Stop()
		y.Stop()
		y.Stop()
	})
}

// --- Context cancellation ---

func (s *YielderSuite) TestContextCancellationStopsYielder() {
	ctx, cancel := context.WithCancel(context.Background())
	y, err := New[int](ctx,
		WithValues(ptrs(1, 2, 3, 4, 5)),
		WithBuffer[int](0),
		WithTimeout[int](10*time.Second),
	)
	s.NoError(err)

	cancel()
	<-y.Done()
	for range y.Results() {
	}

	// err may be context.Canceled, ErrStopped, or nil (if all sent before cancel)
	if y.Err() != nil {
		s.True(
			errors.Is(y.Err(), context.Canceled) || errors.Is(y.Err(), ErrStopped),
			"unexpected error: %v", y.Err(),
		)
	}
}

// --- Timeout ---

func (s *YielderSuite) TestTimeoutStopsYielder() {
	// generator that blocks forever on send (buf=0, nobody reading)
	y, err := New[int](context.Background(),
		WithValues(ptrs(1, 2, 3)),
		WithBuffer[int](0),
		WithTimeout[int](50*time.Millisecond),
	)
	s.NoError(err)

	<-y.Done()
	for range y.Results() {
	}

	s.ErrorIs(y.Err(), ErrStopped)
}

// --- Done channel ---

func (s *YielderSuite) TestDoneClosedOnNormalCompletion() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)))
	s.NoError(err)

	select {
	case <-y.Done():
		// expected
	case <-time.After(2 * time.Second):
		s.Fail("Done channel not closed after completion")
	}
}

// --- Error joining ---

func (s *YielderSuite) TestSetErrJoinsMultipleErrors() {
	y, err := New[int](context.Background(), WithValues(ptrs(1)))
	s.NoError(err)
	<-y.Done()

	first := errors.New("first")
	second := errors.New("second")
	y.setErr(first)
	y.setErr(second)

	s.ErrorIs(y.Err(), first)
	s.ErrorIs(y.Err(), second)
}

// --- helpers ---

func ptrs[T comparable](vals ...T) []*T {
	out := make([]*T, len(vals))
	for i := range vals {
		out[i] = &vals[i]
	}
	return out
}
