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

	y, err := New[int](ctx, WithValues([]int{1}))
	s.Nil(y)
	s.ErrorIs(err, context.Canceled)
}

func (s *YielderSuite) TestNewReturnsErrorWhenFnIsNil() {
	y, err := New[int](context.Background())
	s.Nil(y)
	s.ErrorIs(err, ErrNil)
}

func (s *YielderSuite) TestNewAppliesDefaultTimeout() {
	y, err := New[int](context.Background(), WithValues([]int{1}))
	s.NoError(err)
	defer y.Stop()
	s.Equal(defaultTimeout, y.timeout)
}

func (s *YielderSuite) TestNewAppliesDefaultBufOnNegative() {
	y, err := New[int](context.Background(), WithValues([]int{1}), WithBuffer[int](-5))
	s.NoError(err)
	defer y.Stop()
	s.Equal(defaultBuf, y.buf)
}

func (s *YielderSuite) TestNewRespectsCustomTimeout() {
	y, err := New[int](context.Background(),
		WithValues([]int{1}),
		WithTimeout[int](5*time.Second),
	)
	s.NoError(err)
	defer y.Stop()
	s.Equal(5*time.Second, y.timeout)
}

func (s *YielderSuite) TestNewRespectsCustomBuffer() {
	y, err := New[int](context.Background(), WithValues([]int{1}), WithBuffer[int](10))
	s.NoError(err)
	defer y.Stop()
	s.Equal(10, y.buf)
}

// --- WithValues ---

func (s *YielderSuite) TestWithValuesEmitsAllValues() {
	y, err := New[int](context.Background(), WithValues([]int{1, 2, 3, 4, 5}))
	s.NoError(err)

	var got []int
	for v := range y.Results() {
		got = append(got, v)
	}
	s.Equal([]int{1, 2, 3, 4, 5}, got)
	s.NoError(y.Err())
}

func (s *YielderSuite) TestWithValuesEmptySlice() {
	y, err := New[int](context.Background(), WithValues[int](nil))
	s.NoError(err)

	var got []int
	for v := range y.Results() {
		got = append(got, v)
	}
	s.Empty(got)
	s.NoError(y.Err())
}

// --- WithGeneratorFunc ---

func (s *YielderSuite) TestWithGeneratorFuncEmitsValues() {
	y, err := New[string](context.Background(),
		WithGeneratorFunc(func(_ context.Context) ([]string, error) {
			return []string{"hello", "world"}, nil
		}),
	)
	s.NoError(err)

	var got []string
	for v := range y.Results() {
		got = append(got, v)
	}
	s.Equal([]string{"hello", "world"}, got)
	s.NoError(y.Err())
}

func (s *YielderSuite) TestGeneratorFuncErrorStopsYielder() {
	genErr := errors.New("generator failed")
	y, err := New[int](context.Background(),
		WithGeneratorFunc(func(_ context.Context) ([]int, error) {
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
		WithGeneratorFunc(func(_ context.Context) ([]int, error) {
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
		WithValues([]int{1, 2, 3}),
		WithBuffer[int](0),
		WithTimeout[int](10*time.Second),
	)
	s.NoError(err)

	y.Stop()
	<-y.Done()

	var got []int
	for v := range y.Results() {
		got = append(got, v)
	}
	// may have emitted some or none before stop
	s.LessOrEqual(len(got), 3)
}

func (s *YielderSuite) TestStopIsIdempotent() {
	y, err := New[int](context.Background(), WithValues([]int{1}))
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
		WithValues([]int{1, 2, 3, 4, 5}),
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
		WithValues([]int{1, 2, 3}),
		WithBuffer[int](0),
		WithTimeout[int](50*time.Millisecond),
	)
	s.NoError(err)

	<-y.Done()
	for range y.Results() {
	}

	s.ErrorIs(y.Err(), ErrStopped)
}

// --- WithInputChannel ---

func (s *YielderSuite) TestWithInputChannel() {
	const (
		val1         = 10
		val2         = 20
		val3         = 30
		shortTimeout = 50 * time.Millisecond
		longTimeout  = 10 * time.Second
	)

	tests := []struct {
		name       string
		chanBuf    int
		timeout    time.Duration
		prepare    func(ch chan int, y *Yielder[int])
		wantValues []int
		wantErr    error
	}{
		{
			name:    "ForwardsValuesAndStopsOnInputClosed",
			chanBuf: 3,
			timeout: longTimeout,
			prepare: func(ch chan int, _ *Yielder[int]) {
				ch <- val1
				ch <- val2
				ch <- val3
				close(ch)
			},
			wantValues: []int{val1, val2, val3},
			wantErr:    ErrInputClosed,
		},
		{
			name:    "EmptyInputClosedImmediately",
			chanBuf: 0,
			timeout: longTimeout,
			prepare: func(ch chan int, _ *Yielder[int]) {
				close(ch)
			},
			wantValues: nil,
			wantErr:    ErrInputClosed,
		},
		{
			name:    "StopStopsYielder",
			chanBuf: 0,
			timeout: longTimeout,
			prepare: func(_ chan int, y *Yielder[int]) {
				y.Stop()
			},
			wantValues: nil,
			wantErr:    ErrStopped,
		},
		{
			name:       "ContextDeadlineStopsYielder",
			chanBuf:    0,
			timeout:    shortTimeout,
			prepare:    func(_ chan int, _ *Yielder[int]) {},
			wantValues: nil,
			wantErr:    ErrStopped,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			ch := make(chan int, tc.chanBuf)
			y, err := New[int](context.Background(),
				WithInputChannel[int](ch),
				WithTimeout[int](tc.timeout),
			)
			s.Require().NoError(err)

			tc.prepare(ch, y)

			var got []int
			for v := range y.Results() {
				got = append(got, v)
			}

			s.Equal(tc.wantValues, got)
			s.ErrorIs(y.Err(), tc.wantErr)
		})
	}
}

// --- Done channel ---

func (s *YielderSuite) TestDoneClosedOnNormalCompletion() {
	y, err := New[int](context.Background(), WithValues([]int{1}))
	s.NoError(err)

	select {
	case <-y.Done():
		// expected
	case <-time.After(2 * time.Second):
		s.Fail("Done channel not closed after completion")
	}
}
