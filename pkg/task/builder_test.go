package task

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBuilder(t *testing.T) {
	t.Run("creates empty builder with no options", func(t *testing.T) {
		b := NewBuilder()
		require.NotNil(t, b)
		assert.Empty(t, b.opts)
	})

	t.Run("creates builder with options", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithName("test"),
		)
		require.NotNil(t, b)
		assert.Len(t, b.opts, 2)
	})
}

func TestWithID(t *testing.T) {
	t.Run("sets ID on definition", func(t *testing.T) {
		var d Definition
		opt := WithID(123)
		opt(&d)
		assert.Equal(t, uint64(123), d.ID)
	})
}

func TestWithName(t *testing.T) {
	t.Run("sets Name on definition", func(t *testing.T) {
		var d Definition
		opt := WithName("my-task")
		opt(&d)
		assert.Equal(t, "my-task", d.Name)
	})
}

func TestWithTaskFn(t *testing.T) {
	t.Run("sets TaskFn on definition", func(t *testing.T) {
		var d Definition
		fn := func(r *Run) error { return nil }
		opt := WithTaskFn(fn)
		opt(&d)
		assert.NotNil(t, d.TaskFn)
	})
}

func TestWithMaxRetries(t *testing.T) {
	t.Run("sets MaxRetries on definition", func(t *testing.T) {
		var d Definition
		opt := WithMaxRetries(5)
		opt(&d)
		assert.Equal(t, 5, d.MaxRetries)
	})
}

func TestWithDelay(t *testing.T) {
	t.Run("sets Delay on definition", func(t *testing.T) {
		var d Definition
		opt := WithDelay(100 * time.Millisecond)
		opt(&d)
		assert.Equal(t, 100*time.Millisecond, d.Delay)
	})
}

func TestWithTimeout(t *testing.T) {
	t.Run("sets MaxDuration on definition", func(t *testing.T) {
		var d Definition
		opt := WithTimeout(5 * time.Second)
		opt(&d)
		assert.Equal(t, 5*time.Second, d.MaxDuration)
	})
}

func TestWithHooks(t *testing.T) {
	t.Run("sets Hooks on definition", func(t *testing.T) {
		var d Definition
		hooks := NewStateHooks()
		opt := WithHooks(hooks)
		opt(&d)
		assert.Equal(t, hooks, d.Hooks)
	})
}

func TestBuilder_Build(t *testing.T) {
	t.Run("builds valid definition with all required fields", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithName("test-task"),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(3),
		)

		def, err := b.Build()
		require.NoError(t, err)
		require.NotNil(t, def)
		assert.Equal(t, uint64(1), def.ID)
		assert.Equal(t, "test-task", def.Name)
		assert.NotNil(t, def.TaskFn)
		assert.Equal(t, 3, def.MaxRetries)
	})

	t.Run("returns error when ID is not set", func(t *testing.T) {
		b := NewBuilder(
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(3),
		)

		def, err := b.Build()
		assert.Nil(t, def)
		assert.ErrorIs(t, err, ErrIDNotSet)
	})

	t.Run("returns error when TaskFn is not set", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithMaxRetries(3),
		)

		def, err := b.Build()
		assert.Nil(t, def)
		assert.ErrorIs(t, err, ErrTaskFnNotSet)
	})

	t.Run("returns error when MaxRetries is not set", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithTaskFn(func(r *Run) error { return nil }),
		)

		def, err := b.Build()
		assert.Nil(t, def)
		assert.ErrorIs(t, err, ErrMaxRetriesNotSet)
	})

	t.Run("returns error when MaxRetries is zero", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(0),
		)

		def, err := b.Build()
		assert.Nil(t, def)
		assert.ErrorIs(t, err, ErrMaxRetriesNotSet)
	})

	t.Run("returns error when MaxRetries is negative", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(-1),
		)

		def, err := b.Build()
		assert.Nil(t, def)
		assert.ErrorIs(t, err, ErrMaxRetriesNotSet)
	})

	t.Run("generates default name when not provided", func(t *testing.T) {
		b := NewBuilder(
			WithID(42),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(1),
		)

		def, err := b.Build()
		require.NoError(t, err)
		assert.Equal(t, "task-42", def.Name)
	})

	t.Run("uses provided name when set", func(t *testing.T) {
		b := NewBuilder(
			WithID(42),
			WithName("custom-name"),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(1),
		)

		def, err := b.Build()
		require.NoError(t, err)
		assert.Equal(t, "custom-name", def.Name)
	})

	t.Run("includes optional fields when provided", func(t *testing.T) {
		hooks := NewStateHooks()
		b := NewBuilder(
			WithID(1),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(3),
			WithDelay(100*time.Millisecond),
			WithTimeout(5*time.Second),
			WithHooks(hooks),
		)

		def, err := b.Build()
		require.NoError(t, err)
		assert.Equal(t, 100*time.Millisecond, def.Delay)
		assert.Equal(t, 5*time.Second, def.MaxDuration)
		assert.Equal(t, hooks, def.Hooks)
	})

	t.Run("applies options in order", func(t *testing.T) {
		b := NewBuilder(
			WithID(1),
			WithID(2), // Should override previous
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(3),
		)

		def, err := b.Build()
		require.NoError(t, err)
		assert.Equal(t, uint64(2), def.ID)
	})
}

func TestBuilder_Integration(t *testing.T) {
	t.Run("built definition can be used with New", func(t *testing.T) {
		b := NewBuilder(
			WithID(100),
			WithName("integration-test"),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(2),
			WithTimeout(10*time.Second),
		)

		def, err := b.Build()
		require.NoError(t, err)

		task := New(*def)
		require.NotNil(t, task)
		assert.Equal(t, uint64(100), task.ID())
		assert.Equal(t, "integration-test", task.Name())
	})
}

// Benchmark tests
func BenchmarkNewBuilder(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewBuilder(
			WithID(1),
			WithName("bench-task"),
			WithTaskFn(func(r *Run) error { return nil }),
			WithMaxRetries(3),
		)
	}
}

func BenchmarkBuilder_Build(b *testing.B) {
	builder := NewBuilder(
		WithID(1),
		WithName("bench-task"),
		WithTaskFn(func(r *Run) error { return nil }),
		WithMaxRetries(3),
		WithDelay(100*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = builder.Build()
	}
}
