package retry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// LinearSuite tests the Linear retry strategy.
type LinearSuite struct {
	suite.Suite
}

func TestLinearSuite(t *testing.T) {
	suite.Run(t, new(LinearSuite))
}

func (s *LinearSuite) TestReturnsLinearlyIncreasingDelay() {
	strategy := NewLinear(WithBaseDelay(100 * time.Millisecond))

	s.Equal(100*time.Millisecond, strategy.Delay(0))
	s.Equal(200*time.Millisecond, strategy.Delay(1))
	s.Equal(300*time.Millisecond, strategy.Delay(2))
	s.Equal(400*time.Millisecond, strategy.Delay(3))
}

func (s *LinearSuite) TestCapsAtMaxDelay() {
	strategy := NewLinear(
		WithBaseDelay(100*time.Millisecond),
		WithMaxDelay(250*time.Millisecond),
	)

	s.Equal(100*time.Millisecond, strategy.Delay(0))
	s.Equal(200*time.Millisecond, strategy.Delay(1))
	s.Equal(250*time.Millisecond, strategy.Delay(2))
	s.Equal(250*time.Millisecond, strategy.Delay(3))
}

func (s *LinearSuite) TestWithJitterReturnsValueInRange() {
	strategy := NewLinear(
		WithBaseDelay(100*time.Millisecond),
		WithJitter(true),
	)

	for i := 0; i < 100; i++ {
		d := strategy.Delay(1)
		s.GreaterOrEqual(d, time.Duration(0))
		s.LessOrEqual(d, 200*time.Millisecond)
	}
}

func (s *LinearSuite) TestZeroBaseDelayReturnsZero() {
	strategy := NewLinear(WithBaseDelay(0))
	s.Equal(time.Duration(0), strategy.Delay(5))
}

func (s *LinearSuite) TestLargeAttemptCapsAtMaxDelay() {
	strategy := NewLinear(
		WithBaseDelay(100*time.Millisecond),
		WithMaxDelay(1*time.Second),
	)
	s.Equal(1*time.Second, strategy.Delay(100))
}

// ExponentialBackoffSuite tests the ExponentialBackoff retry strategy.
type ExponentialBackoffSuite struct {
	suite.Suite
}

func TestExponentialBackoffSuite(t *testing.T) {
	suite.Run(t, new(ExponentialBackoffSuite))
}

func (s *ExponentialBackoffSuite) TestReturnsExponentiallyIncreasingDelay() {
	strategy := NewExponentialBackoff(WithBaseDelay(100 * time.Millisecond))

	s.Equal(100*time.Millisecond, strategy.Delay(0))  // 100ms * 2^0 = 100ms
	s.Equal(200*time.Millisecond, strategy.Delay(1))  // 100ms * 2^1 = 200ms
	s.Equal(400*time.Millisecond, strategy.Delay(2))  // 100ms * 2^2 = 400ms
	s.Equal(800*time.Millisecond, strategy.Delay(3))  // 100ms * 2^3 = 800ms
	s.Equal(1600*time.Millisecond, strategy.Delay(4)) // 100ms * 2^4 = 1600ms
}

func (s *ExponentialBackoffSuite) TestCapsAtMaxDelay() {
	strategy := NewExponentialBackoff(
		WithBaseDelay(100*time.Millisecond),
		WithMaxDelay(500*time.Millisecond),
	)

	s.Equal(100*time.Millisecond, strategy.Delay(0))
	s.Equal(200*time.Millisecond, strategy.Delay(1))
	s.Equal(400*time.Millisecond, strategy.Delay(2))
	s.Equal(500*time.Millisecond, strategy.Delay(3)) // capped
	s.Equal(500*time.Millisecond, strategy.Delay(4)) // capped
}

func (s *ExponentialBackoffSuite) TestWithJitterReturnsValueInRange() {
	strategy := NewExponentialBackoff(
		WithBaseDelay(100*time.Millisecond),
		WithJitter(true),
	)

	for i := 0; i < 100; i++ {
		d := strategy.Delay(2)
		s.GreaterOrEqual(d, time.Duration(0))
		s.LessOrEqual(d, 400*time.Millisecond)
	}
}

func (s *ExponentialBackoffSuite) TestHandlesLargeAttemptNumbersWithoutOverflow() {
	strategy := NewExponentialBackoff(
		WithBaseDelay(100*time.Millisecond),
		WithMaxDelay(1*time.Second),
	)

	d := strategy.Delay(100)
	s.Equal(1*time.Second, d)
}

func (s *ExponentialBackoffSuite) TestAttempt63ReturnsMaxDelay() {
	strategy := NewExponentialBackoff(
		WithBaseDelay(100*time.Millisecond),
		WithMaxDelay(5*time.Second),
	)
	s.Equal(5*time.Second, strategy.Delay(63))
}

func (s *ExponentialBackoffSuite) TestZeroBaseDelayReturnsZero() {
	strategy := NewExponentialBackoff(WithBaseDelay(0))
	s.Equal(time.Duration(0), strategy.Delay(0))
}

// HelpersSuite tests internal helper functions and shared option behavior.
type HelpersSuite struct {
	suite.Suite
}

func TestHelpersSuite(t *testing.T) {
	suite.Run(t, new(HelpersSuite))
}

func (s *HelpersSuite) TestOptionsOverrideDefaults() {
	strategy := NewLinear(
		WithBaseDelay(500*time.Millisecond),
		WithMaxDelay(10*time.Second),
	)
	s.Equal(500*time.Millisecond, strategy.Delay(0))
}

func (s *HelpersSuite) TestNilOptionsAreIgnored() {
	strategy := NewLinear(nil, WithBaseDelay(200*time.Millisecond))
	s.Equal(200*time.Millisecond, strategy.Delay(0))
}

func (s *HelpersSuite) TestApplyJitterZeroDelayReturnsZero() {
	s.Equal(time.Duration(0), applyJitter(0))
}

func (s *HelpersSuite) TestApplyJitterNegativeDelayReturnsZero() {
	s.Equal(time.Duration(0), applyJitter(-1*time.Second))
}

func (s *HelpersSuite) TestCapDelayDoesNotCapBelowMax() {
	s.Equal(100*time.Millisecond, capDelay(100*time.Millisecond, 1*time.Second))
}

func (s *HelpersSuite) TestCapDelayCapsAtMax() {
	s.Equal(1*time.Second, capDelay(5*time.Second, 1*time.Second))
}

// Benchmark tests
func BenchmarkLinear_Delay(b *testing.B) {
	s := NewLinear(WithBaseDelay(100 * time.Millisecond))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Delay(i % 10)
	}
}

func BenchmarkExponentialBackoff_Delay(b *testing.B) {
	s := NewExponentialBackoff(WithBaseDelay(100 * time.Millisecond))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Delay(i % 10)
	}
}
