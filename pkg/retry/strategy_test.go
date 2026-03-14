package retry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConstant(t *testing.T) {
	t.Run("returns base delay for all attempts", func(t *testing.T) {
		s := Constant(WithBaseDelay(200 * time.Millisecond))

		for attempt := 0; attempt < 5; attempt++ {
			assert.Equal(t, 200*time.Millisecond, s.Delay(attempt))
		}
	})

	t.Run("uses default base delay", func(t *testing.T) {
		s := Constant()
		assert.Equal(t, defaultBaseDelay, s.Delay(0))
	})

	t.Run("caps at max delay", func(t *testing.T) {
		s := Constant(
			WithBaseDelay(500*time.Millisecond),
			WithMaxDelay(200*time.Millisecond),
		)
		assert.Equal(t, 200*time.Millisecond, s.Delay(0))
	})

	t.Run("with jitter returns value in range", func(t *testing.T) {
		s := Constant(
			WithBaseDelay(100*time.Millisecond),
			WithJitter(true),
		)

		for i := 0; i < 100; i++ {
			d := s.Delay(0)
			assert.GreaterOrEqual(t, d, time.Duration(0))
			assert.LessOrEqual(t, d, 100*time.Millisecond)
		}
	})

	t.Run("zero base delay returns zero", func(t *testing.T) {
		s := Constant(WithBaseDelay(0))
		assert.Equal(t, time.Duration(0), s.Delay(0))
	})
}

func TestLinear(t *testing.T) {
	t.Run("returns linearly increasing delay", func(t *testing.T) {
		s := Linear(WithBaseDelay(100 * time.Millisecond))

		assert.Equal(t, 100*time.Millisecond, s.Delay(0))
		assert.Equal(t, 200*time.Millisecond, s.Delay(1))
		assert.Equal(t, 300*time.Millisecond, s.Delay(2))
		assert.Equal(t, 400*time.Millisecond, s.Delay(3))
	})

	t.Run("caps at max delay", func(t *testing.T) {
		s := Linear(
			WithBaseDelay(100*time.Millisecond),
			WithMaxDelay(250*time.Millisecond),
		)

		assert.Equal(t, 100*time.Millisecond, s.Delay(0))
		assert.Equal(t, 200*time.Millisecond, s.Delay(1))
		assert.Equal(t, 250*time.Millisecond, s.Delay(2))
		assert.Equal(t, 250*time.Millisecond, s.Delay(3))
	})

	t.Run("with jitter returns value in range", func(t *testing.T) {
		s := Linear(
			WithBaseDelay(100*time.Millisecond),
			WithJitter(true),
		)

		for i := 0; i < 100; i++ {
			d := s.Delay(1)
			assert.GreaterOrEqual(t, d, time.Duration(0))
			assert.LessOrEqual(t, d, 200*time.Millisecond)
		}
	})

	t.Run("zero base delay returns zero", func(t *testing.T) {
		s := Linear(WithBaseDelay(0))
		assert.Equal(t, time.Duration(0), s.Delay(5))
	})

	t.Run("large attempt caps at max delay", func(t *testing.T) {
		s := Linear(
			WithBaseDelay(100*time.Millisecond),
			WithMaxDelay(1*time.Second),
		)
		assert.Equal(t, 1*time.Second, s.Delay(100))
	})
}

func TestExponentialBackoff(t *testing.T) {
	t.Run("returns exponentially increasing delay", func(t *testing.T) {
		s := ExponentialBackoff(WithBaseDelay(100 * time.Millisecond))

		assert.Equal(t, 100*time.Millisecond, s.Delay(0))  // 100ms * 2^0 = 100ms
		assert.Equal(t, 200*time.Millisecond, s.Delay(1))  // 100ms * 2^1 = 200ms
		assert.Equal(t, 400*time.Millisecond, s.Delay(2))  // 100ms * 2^2 = 400ms
		assert.Equal(t, 800*time.Millisecond, s.Delay(3))  // 100ms * 2^3 = 800ms
		assert.Equal(t, 1600*time.Millisecond, s.Delay(4)) // 100ms * 2^4 = 1600ms
	})

	t.Run("caps at max delay", func(t *testing.T) {
		s := ExponentialBackoff(
			WithBaseDelay(100*time.Millisecond),
			WithMaxDelay(500*time.Millisecond),
		)

		assert.Equal(t, 100*time.Millisecond, s.Delay(0))
		assert.Equal(t, 200*time.Millisecond, s.Delay(1))
		assert.Equal(t, 400*time.Millisecond, s.Delay(2))
		assert.Equal(t, 500*time.Millisecond, s.Delay(3)) // capped
		assert.Equal(t, 500*time.Millisecond, s.Delay(4)) // capped
	})

	t.Run("with jitter returns value in range", func(t *testing.T) {
		s := ExponentialBackoff(
			WithBaseDelay(100*time.Millisecond),
			WithJitter(true),
		)

		for i := 0; i < 100; i++ {
			d := s.Delay(2)
			assert.GreaterOrEqual(t, d, time.Duration(0))
			assert.LessOrEqual(t, d, 400*time.Millisecond)
		}
	})

	t.Run("handles large attempt numbers without overflow", func(t *testing.T) {
		s := ExponentialBackoff(
			WithBaseDelay(100*time.Millisecond),
			WithMaxDelay(1*time.Second),
		)

		d := s.Delay(100)
		assert.Equal(t, 1*time.Second, d)
	})

	t.Run("attempt 63 returns max delay", func(t *testing.T) {
		s := ExponentialBackoff(
			WithBaseDelay(100*time.Millisecond),
			WithMaxDelay(5*time.Second),
		)
		assert.Equal(t, 5*time.Second, s.Delay(63))
	})

	t.Run("zero base delay returns zero", func(t *testing.T) {
		s := ExponentialBackoff(WithBaseDelay(0))
		assert.Equal(t, time.Duration(0), s.Delay(0))
	})
}

func TestCustomOptions(t *testing.T) {
	t.Run("options override defaults", func(t *testing.T) {
		s := Constant(
			WithBaseDelay(500*time.Millisecond),
			WithMaxDelay(10*time.Second),
		)
		assert.Equal(t, 500*time.Millisecond, s.Delay(0))
	})

	t.Run("nil options are ignored", func(t *testing.T) {
		s := Constant(nil, WithBaseDelay(200*time.Millisecond))
		assert.Equal(t, 200*time.Millisecond, s.Delay(0))
	})
}

func TestApplyJitter(t *testing.T) {
	t.Run("zero delay returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), applyJitter(0))
	})

	t.Run("negative delay returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), applyJitter(-1*time.Second))
	})
}

func TestCapDelay(t *testing.T) {
	t.Run("does not cap below max", func(t *testing.T) {
		assert.Equal(t, 100*time.Millisecond, capDelay(100*time.Millisecond, 1*time.Second))
	})

	t.Run("caps at max", func(t *testing.T) {
		assert.Equal(t, 1*time.Second, capDelay(5*time.Second, 1*time.Second))
	})
}

// Benchmark tests
func BenchmarkConstant_Delay(b *testing.B) {
	s := Constant(WithBaseDelay(100 * time.Millisecond))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Delay(i % 10)
	}
}

func BenchmarkLinear_Delay(b *testing.B) {
	s := Linear(WithBaseDelay(100 * time.Millisecond))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Delay(i % 10)
	}
}

func BenchmarkExponentialBackoff_Delay(b *testing.B) {
	s := ExponentialBackoff(WithBaseDelay(100 * time.Millisecond))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Delay(i % 10)
	}
}
