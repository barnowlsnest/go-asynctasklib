package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("creates task with default values", func(t *testing.T) {
		task := New(Definition{
			ID: "test-task",
		})

		assert.Equal(t, "test-task", task.ID())
		assert.True(t, task.IsCreated())
		assert.Equal(t, defaultTimeout, task.timeout)
	})

	t.Run("creates task with custom values", func(t *testing.T) {
		taskFn := func(r *Run) error { return nil }
		hooks := NewStateHooks()
		task := New(Definition{
			ID:          "custom-task",
			TaskFn:      taskFn,
			Hooks:       hooks,
			Delay:       100 * time.Millisecond,
			MaxDuration: 5 * time.Second,
			MaxRetries:  3,
		})

		assert.Equal(t, "custom-task", task.ID())
		assert.Equal(t, 100*time.Millisecond, task.delay)
		assert.Equal(t, 5*time.Second, task.timeout)
		assert.Equal(t, 3, task.maxRetries)
	})
}

func TestTask_Go(t *testing.T) {
	t.Run("executes successful task", func(t *testing.T) {
		executed := false
		task := New(Definition{
			ID: "success-task",
			TaskFn: func(r *Run) error {
				executed = true
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()

		assert.True(t, executed)
		assert.True(t, task.IsDone())
	})

	t.Run("fails task with error", func(t *testing.T) {
		expectedErr := errors.New("task error")
		task := New(Definition{
			ID: "error-task",
			TaskFn: func(r *Run) error {
				return expectedErr
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()

		assert.True(t, task.IsFailed())
		assert.ErrorIs(t, task.Err(), expectedErr)
	})

	t.Run("returns error for nil context", func(t *testing.T) {
		task := New(Definition{
			ID:     "nil-ctx-task",
			TaskFn: func(r *Run) error { return nil },
		})

		err := task.Go(nil)
		assert.ErrorIs(t, err, ErrNilCtx)
	})

	t.Run("returns error for task already in progress", func(t *testing.T) {
		task := New(Definition{
			ID: "in-progress-task",
			TaskFn: func(r *Run) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		// Try to start again while in progress
		err = task.Go(ctx)
		assert.ErrorIs(t, err, ErrTaskInProgress)

		task.Await()
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		task := New(Definition{
			ID:    "cancel-task",
			Delay: 50 * time.Millisecond,
			TaskFn: func(r *Run) error {
				time.Sleep(200 * time.Millisecond)
				return nil
			},
		})

		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		err := task.Go(ctx)
		assert.ErrorIs(t, err, ErrCancelledTask)
	})

	t.Run("respects delay", func(t *testing.T) {
		start := time.Now()
		task := New(Definition{
			ID:     "delay-task",
			Delay:  100 * time.Millisecond,
			TaskFn: func(r *Run) error { return nil },
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()
		elapsed := time.Since(start)

		assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
	})

	t.Run("handles timeout", func(t *testing.T) {
		task := New(Definition{
			ID:          "timeout-task",
			MaxDuration: 50 * time.Millisecond,
			TaskFn: func(r *Run) error {
				time.Sleep(200 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()

		assert.True(t, task.IsFailed())
		assert.ErrorIs(t, task.Err(), ErrTaskTimeout)
	})

	t.Run("handles panic in task function", func(t *testing.T) {
		task := New(Definition{
			ID: "panic-task",
			TaskFn: func(r *Run) error {
				panic("test panic")
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()

		assert.True(t, task.IsFailed())
		assert.NotNil(t, task.Err())
	})

	t.Run("provides Run context helpers", func(t *testing.T) {
		var (
			gotID       string
			gotContext  context.Context
			gotCancel   context.CancelFunc
			gotCallback func()
		)

		task := New(Definition{
			ID: "run-helpers-task",
			TaskFn: func(r *Run) error {
				gotID = r.ID()
				gotContext = r.Context()
				gotCancel = r.Cancel
				gotCallback = r.Callback
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)
		task.Await()

		assert.Equal(t, "run-helpers-task", gotID)
		assert.NotNil(t, gotContext)
		assert.NotNil(t, gotCancel)
		assert.NotNil(t, gotCallback)
	})
}

func TestTask_GoRetry(t *testing.T) {
	t.Run("retries failed task", func(t *testing.T) {
		attempts := 0
		task := New(Definition{
			ID:         "retry-task",
			MaxRetries: 3,
			TaskFn: func(r *Run) error {
				attempts++
				if attempts < 3 {
					return errors.New("temporary error")
				}
				return nil
			},
		})

		ctx := context.Background()
		err := task.GoRetry(ctx)
		require.NoError(t, err)

		assert.Equal(t, 3, attempts)
		assert.True(t, task.IsDone())
	})

	t.Run("exceeds max retries", func(t *testing.T) {
		attempts := 0
		task := New(Definition{
			ID:         "max-retry-task",
			MaxRetries: 2,
			TaskFn: func(r *Run) error {
				attempts++
				return errors.New("persistent error")
			},
		})

		ctx := context.Background()
		err := task.GoRetry(ctx)
		assert.ErrorIs(t, err, ErrMaxRetriesExceeded)
		assert.Equal(t, 2, attempts)
	})

	t.Run("returns error when max retries not set", func(t *testing.T) {
		task := New(Definition{
			ID:     "no-retry-task",
			TaskFn: func(r *Run) error { return nil },
		})

		ctx := context.Background()
		err := task.GoRetry(ctx)
		assert.ErrorIs(t, err, ErrMaxRetriesNotSet)
	})

	t.Run("tracks attempts correctly", func(t *testing.T) {
		attempts := 0
		task := New(Definition{
			ID:         "attempts-task",
			MaxRetries: 4,
			TaskFn: func(r *Run) error {
				attempts++
				if attempts < 4 {
					return errors.New("retry")
				}
				return nil
			},
		})

		ctx := context.Background()
		err := task.GoRetry(ctx)
		require.NoError(t, err)

		assert.Equal(t, uint32(3), task.attempts.Load(), "expected 3 retry attempts after first attempt")
	})
}

func TestTask_Cancel(t *testing.T) {
	t.Run("cancels running task", func(t *testing.T) {
		task := New(Definition{
			ID: "cancel-running-task",
			TaskFn: func(r *Run) error {
				select {
				case <-r.Context().Done():
					return r.Context().Err()
				case <-time.After(1 * time.Second):
					return nil
				}
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		time.Sleep(10 * time.Millisecond)

		task.Cancel()
		task.Await()

		assert.True(t, task.IsCanceled())
	})
}

func TestTask_StateCheckers(t *testing.T) {
	tests := []struct {
		name     string
		state    uint32
		checker  func(*Task) bool
		expected bool
	}{
		{"IsCreated", CREATED, (*Task).IsCreated, true},
		{"IsPending", PENDING, (*Task).IsPending, true},
		{"IsStarted", STARTED, (*Task).IsStarted, true},
		{"IsDone", DONE, (*Task).IsDone, true},
		{"IsFailed", FAILED, (*Task).IsFailed, true},
		{"IsCanceled", CANCELED, (*Task).IsCanceled, true},
		{"IsEnd with DONE", DONE, (*Task).IsEnd, true},
		{"IsEnd with FAILED", FAILED, (*Task).IsEnd, true},
		{"IsEnd with CANCELED", CANCELED, (*Task).IsEnd, true},
		{"IsEnd with CREATED", CREATED, (*Task).IsEnd, false},
		{"IsInProgress with STARTED", STARTED, (*Task).IsInProgress, true},
		{"IsInProgress with PENDING", PENDING, (*Task).IsInProgress, true},
		{"IsInProgress with DONE", DONE, (*Task).IsInProgress, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := New(Definition{ID: "test"})
			task.state.Store(tt.state)

			result := tt.checker(task)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTask_Hooks(t *testing.T) {
	t.Run("calls hooks during lifecycle", func(t *testing.T) {
		var (
			createdCalled bool
			pendingCalled bool
			startedCalled bool
			doneCalled    bool
			mu            sync.Mutex
		)

		hooks := NewStateHooks(
			WhenCreated(func(id string, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				createdCalled = true
			}),
			WhenPending(func(id string, when time.Time, attempt int) {
				mu.Lock()
				defer mu.Unlock()
				pendingCalled = true
			}),
			WhenStarted(func(id string, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				startedCalled = true
			}),
			WhenDone(func(id string, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				doneCalled = true
			}),
		)

		task := New(Definition{
			ID:     "hook-task",
			Hooks:  hooks,
			TaskFn: func(r *Run) error { return nil },
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		task.Await()

		mu.Lock()
		defer mu.Unlock()

		assert.True(t, createdCalled, "expected created hook to be called")
		assert.True(t, pendingCalled, "expected pending hook to be called")
		assert.True(t, startedCalled, "expected started hook to be called")
		assert.True(t, doneCalled, "expected done hook to be called")
	})

	t.Run("calls failed hook on error", func(t *testing.T) {
		var (
			failedCalled bool
			mu           sync.Mutex
		)

		hooks := NewStateHooks(
			WhenFailed(func(id string, when time.Time, err error) {
				mu.Lock()
				defer mu.Unlock()
				failedCalled = true
			}),
		)

		task := New(Definition{
			ID:     "failed-hook-task",
			Hooks:  hooks,
			TaskFn: func(r *Run) error { return errors.New("error") },
		})

		ctx := context.Background()
		_ = task.Go(ctx)
		task.Await()

		mu.Lock()
		defer mu.Unlock()

		assert.True(t, failedCalled, "expected failed hook to be called")
	})

	t.Run("callback hook is invoked", func(t *testing.T) {
		callbackCalled := false
		var mu sync.Mutex

		hooks := NewStateHooks(
			FromTaskFn(func(id string, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				callbackCalled = true
			}),
		)

		task := New(Definition{
			ID:    "callback-task",
			Hooks: hooks,
			TaskFn: func(r *Run) error {
				r.Callback()
				return nil
			},
		})

		ctx := context.Background()
		_ = task.Go(ctx)
		task.Await()

		mu.Lock()
		defer mu.Unlock()

		assert.True(t, callbackCalled, "expected callback hook to be called")
	})
}

// Race condition tests
func TestTask_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent state reads", func(t *testing.T) {
		task := New(Definition{
			ID: "concurrent-read-task",
			TaskFn: func(r *Run) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = task.State()
				_ = task.IsCreated()
				_ = task.IsPending()
				_ = task.IsStarted()
				_ = task.IsDone()
				_ = task.IsFailed()
				_ = task.IsCanceled()
				_ = task.IsEnd()
				_ = task.IsInProgress()
			}()
		}

		wg.Wait()
		task.Await()
	})

	t.Run("concurrent error reads", func(t *testing.T) {
		task := New(Definition{
			ID: "concurrent-err-task",
			TaskFn: func(r *Run) error {
				time.Sleep(50 * time.Millisecond)
				return errors.New("test error")
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = task.Err()
			}()
		}

		wg.Wait()
		task.Await()
	})

	t.Run("multiple goroutines await", func(t *testing.T) {
		task := New(Definition{
			ID: "multi-await-task",
			TaskFn: func(r *Run) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task.Await()
			}()
		}

		wg.Wait()
	})

	t.Run("concurrent cancel calls", func(t *testing.T) {
		task := New(Definition{
			ID: "concurrent-cancel-task",
			TaskFn: func(r *Run) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task.Cancel()
			}()
		}

		wg.Wait()
		task.Await()
	})

	t.Run("stress test with multiple operations", func(t *testing.T) {
		var counter atomic.Int32

		task := New(Definition{
			ID: "stress-task",
			TaskFn: func(r *Run) error {
				counter.Add(1)
				time.Sleep(10 * time.Millisecond)
				return nil
			},
		})

		ctx := context.Background()
		err := task.Go(ctx)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					_ = task.State()
					_ = task.ID()
					_ = task.Err()
				}
			}()
		}

		wg.Wait()
		task.Await()

		assert.Equal(t, int32(1), counter.Load(), "expected task to execute once")
	})

	t.Run("concurrent attempts reads during retry", func(t *testing.T) {
		attemptCount := 0
		task := New(Definition{
			ID:         "concurrent-attempts-task",
			MaxRetries: 5,
			TaskFn: func(r *Run) error {
				attemptCount++
				if attemptCount < 3 {
					return errors.New("retry")
				}
				return nil
			},
		})

		ctx := context.Background()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = task.GoRetry(ctx)
		}()

		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = task.attempts.Load()
			}()
		}

		wg.Wait()
	})
}

// Benchmark tests
func BenchmarkTask_Go(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := New(Definition{
			ID:     "bench-task",
			TaskFn: func(r *Run) error { return nil },
		})
		_ = task.Go(ctx)
		task.Await()
	}
}

func BenchmarkTask_StateChecks(b *testing.B) {
	task := New(Definition{ID: "bench-state-task"})
	task.state.Store(STARTED)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = task.IsCreated()
		_ = task.IsPending()
		_ = task.IsStarted()
		_ = task.IsDone()
		_ = task.IsFailed()
		_ = task.IsCanceled()
		_ = task.IsEnd()
		_ = task.IsInProgress()
	}
}

func BenchmarkTask_ConcurrentStateReads(b *testing.B) {
	task := New(Definition{
		ID: "bench-concurrent-task",
		TaskFn: func(r *Run) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	})

	ctx := context.Background()
	_ = task.Go(ctx)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = task.State()
			_ = task.IsInProgress()
		}
	})

	task.Await()
}

func BenchmarkTask_GoRetry(b *testing.B) {
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		attempts := 0
		task := New(Definition{
			ID:         "bench-retry-task",
			MaxRetries: 3,
			TaskFn: func(r *Run) error {
				attempts++
				if attempts < 2 {
					return errors.New("retry")
				}
				return nil
			},
		})

		_ = task.GoRetry(ctx)
	}
}

func BenchmarkTask_WithHooks(b *testing.B) {
	hooks := NewStateHooks(
		WhenStarted(func(id string, when time.Time) {}),
		WhenDone(func(id string, when time.Time) {}),
	)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := New(Definition{
			ID:     "bench-hook-task",
			Hooks:  hooks,
			TaskFn: func(r *Run) error { return nil },
		})

		_ = task.Go(ctx)
		task.Await()
	}
}

func BenchmarkTask_New(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New(Definition{
			ID:          "bench-new-task",
			TaskFn:      func(r *Run) error { return nil },
			Delay:       100 * time.Millisecond,
			MaxDuration: 5 * time.Second,
			MaxRetries:  3,
		})
	}
}

func BenchmarkTask_AtomicOperations(b *testing.B) {
	task := New(Definition{ID: "bench-atomic-task"})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			task.state.Load()
			task.attempts.Load()
		}
	})
}
