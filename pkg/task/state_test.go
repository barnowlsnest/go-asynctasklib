package task

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStateHooks(t *testing.T) {
	t.Run("creates hooks with default no-op functions", func(t *testing.T) {
		hooks := NewStateHooks()

		require.NotNil(t, hooks)

		// These should not panic
		assert.NotPanics(t, func() {
			hooks.onCreated(1, time.Now())
			hooks.onStarted(1, time.Now())
			hooks.onDone(1, time.Now())
			hooks.onTaskFn(1, time.Now())
			hooks.onFailed(1, time.Now(), nil)
			hooks.onPending(1, time.Now(), 1)
			hooks.onCanceled(1, time.Now())
		})
	})

	t.Run("applies options correctly", func(t *testing.T) {
		var called bool
		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				called = true
			}),
		)

		hooks.onCreated(1, time.Now())

		assert.True(t, called)
	})

	t.Run("applies multiple options", func(t *testing.T) {
		var (
			createdCalled bool
			startedCalled bool
			doneCalled    bool
		)

		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				createdCalled = true
			}),
			WhenStarted(func(id uint64, when time.Time) {
				startedCalled = true
			}),
			WhenDone(func(id uint64, when time.Time) {
				doneCalled = true
			}),
		)

		hooks.onCreated(1, time.Now())
		hooks.onStarted(1, time.Now())
		hooks.onDone(1, time.Now())

		assert.True(t, createdCalled)
		assert.True(t, startedCalled)
		assert.True(t, doneCalled)
	})
}

func TestWhenCreated(t *testing.T) {
	t.Run("sets created hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
		)

		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				gotID = id
				gotWhen = when
			}),
		)

		testTime := time.Now().UTC()
		hooks.onCreated(123, testTime)

		assert.Equal(t, uint64(123), gotID)
		assert.Equal(t, testTime, gotWhen)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				panic("test panic in created hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onCreated(1, time.Now())
		})
	})
}

func TestWhenStarted(t *testing.T) {
	t.Run("sets started hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
		)

		hooks := NewStateHooks(
			WhenStarted(func(id uint64, when time.Time) {
				gotID = id
				gotWhen = when
			}),
		)

		testTime := time.Now().UTC()
		hooks.onStarted(456, testTime)

		assert.Equal(t, uint64(456), gotID)
		assert.Equal(t, testTime, gotWhen)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenStarted(func(id uint64, when time.Time) {
				panic("test panic in started hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onStarted(1, time.Now())
		})
	})
}

func TestWhenDone(t *testing.T) {
	t.Run("sets done hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
		)

		hooks := NewStateHooks(
			WhenDone(func(id uint64, when time.Time) {
				gotID = id
				gotWhen = when
			}),
		)

		testTime := time.Now().UTC()
		hooks.onDone(789, testTime)

		assert.Equal(t, uint64(789), gotID)
		assert.Equal(t, testTime, gotWhen)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenDone(func(id uint64, when time.Time) {
				panic("test panic in done hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onDone(1, time.Now())
		})
	})
}

func TestFromTaskFn(t *testing.T) {
	t.Run("sets task function hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
		)

		hooks := NewStateHooks(
			FromTaskFn(func(id uint64, when time.Time) {
				gotID = id
				gotWhen = when
			}),
		)

		testTime := time.Now().UTC()
		hooks.onTaskFn(101, testTime)

		assert.Equal(t, uint64(101), gotID)
		assert.Equal(t, testTime, gotWhen)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			FromTaskFn(func(id uint64, when time.Time) {
				panic("test panic in taskfn hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onTaskFn(1, time.Now())
		})
	})
}

func TestWhenFailed(t *testing.T) {
	t.Run("sets failed hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
			gotErr  error
		)

		hooks := NewStateHooks(
			WhenFailed(func(id uint64, when time.Time, err error) {
				gotID = id
				gotWhen = when
				gotErr = err
			}),
		)

		testTime := time.Now().UTC()
		testErr := ErrTaskTimeout
		hooks.onFailed(202, testTime, testErr)

		assert.Equal(t, uint64(202), gotID)
		assert.Equal(t, testTime, gotWhen)
		assert.Equal(t, testErr, gotErr)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenFailed(func(id uint64, when time.Time, err error) {
				panic("test panic in failed hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onFailed(1, time.Now(), nil)
		})
	})
}

func TestWhenPending(t *testing.T) {
	t.Run("sets pending hook", func(t *testing.T) {
		var (
			gotID      uint64
			gotWhen    time.Time
			gotAttempt int
		)

		hooks := NewStateHooks(
			WhenPending(func(id uint64, when time.Time, attempt int) {
				gotID = id
				gotWhen = when
				gotAttempt = attempt
			}),
		)

		testTime := time.Now().UTC()
		hooks.onPending(303, testTime, 3)

		assert.Equal(t, uint64(303), gotID)
		assert.Equal(t, testTime, gotWhen)
		assert.Equal(t, 3, gotAttempt)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenPending(func(id uint64, when time.Time, attempt int) {
				panic("test panic in pending hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onPending(1, time.Now(), 1)
		})
	})
}

func TestWhenCanceled(t *testing.T) {
	t.Run("sets canceled hook", func(t *testing.T) {
		var (
			gotID   uint64
			gotWhen time.Time
		)

		hooks := NewStateHooks(
			WhenCanceled(func(id uint64, when time.Time) {
				gotID = id
				gotWhen = when
			}),
		)

		testTime := time.Now().UTC()
		hooks.onCanceled(404, testTime)

		assert.Equal(t, uint64(404), gotID)
		assert.Equal(t, testTime, gotWhen)
	})

	t.Run("catches panic in hook", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenCanceled(func(id uint64, when time.Time) {
				panic("test panic in canceled hook")
			}),
		)

		assert.NotPanics(t, func() {
			hooks.onCanceled(1, time.Now())
		})
	})
}

func TestCatchPanic(t *testing.T) {
	t.Run("silently catches panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			func() {
				defer catchPanic()
				panic("test panic")
			}()
		})
	})

	t.Run("allows normal execution", func(t *testing.T) {
		executed := false

		func() {
			defer catchPanic()
			executed = true
		}()

		assert.True(t, executed)
	})
}

func TestStateConstants(t *testing.T) {
	t.Run("state constants have unique values", func(t *testing.T) {
		states := []uint32{CREATED, PENDING, STARTED, DONE, FAILED, CANCELED}
		seen := make(map[uint32]bool)

		for _, state := range states {
			assert.False(t, seen[state], "duplicate state value: %d", state)
			seen[state] = true
		}
	})

	t.Run("state constants are sequential", func(t *testing.T) {
		assert.Equal(t, 0, CREATED)
		assert.Equal(t, 1, PENDING)
		assert.Equal(t, 2, STARTED)
		assert.Equal(t, 3, DONE)
		assert.Equal(t, 4, FAILED)
		assert.Equal(t, 5, CANCELED)
	})
}

// Concurrent access tests for hooks
func TestStateHooks_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent hook calls are safe", func(t *testing.T) {
		var (
			counter int
			mu      sync.Mutex
		)

		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				counter++
			}),
		)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				hooks.onCreated(1, time.Now())
			}()
		}

		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 100, counter)
	})

	t.Run("concurrent panic-inducing hooks don't crash", func(t *testing.T) {
		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				panic("concurrent panic test")
			}),
		)

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NotPanics(t, func() {
					hooks.onCreated(1, time.Now())
				})
			}()
		}

		wg.Wait()
	})

	t.Run("multiple hooks called concurrently", func(t *testing.T) {
		var (
			createdCount  int
			startedCount  int
			doneCount     int
			failedCount   int
			pendingCount  int
			canceledCount int
			taskFnCount   int
			mu            sync.Mutex
		)

		hooks := NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				createdCount++
			}),
			WhenStarted(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				startedCount++
			}),
			WhenDone(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				doneCount++
			}),
			WhenFailed(func(id uint64, when time.Time, err error) {
				mu.Lock()
				defer mu.Unlock()
				failedCount++
			}),
			WhenPending(func(id uint64, when time.Time, attempt int) {
				mu.Lock()
				defer mu.Unlock()
				pendingCount++
			}),
			WhenCanceled(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				canceledCount++
			}),
			FromTaskFn(func(id uint64, when time.Time) {
				mu.Lock()
				defer mu.Unlock()
				taskFnCount++
			}),
		)

		var wg sync.WaitGroup
		iterations := 20

		for i := 0; i < iterations; i++ {
			wg.Add(7)
			go func() {
				defer wg.Done()
				hooks.onCreated(1, time.Now())
			}()
			go func() {
				defer wg.Done()
				hooks.onStarted(1, time.Now())
			}()
			go func() {
				defer wg.Done()
				hooks.onDone(1, time.Now())
			}()
			go func() {
				defer wg.Done()
				hooks.onFailed(1, time.Now(), nil)
			}()
			go func() {
				defer wg.Done()
				hooks.onPending(1, time.Now(), 1)
			}()
			go func() {
				defer wg.Done()
				hooks.onCanceled(1, time.Now())
			}()
			go func() {
				defer wg.Done()
				hooks.onTaskFn(1, time.Now())
			}()
		}

		wg.Wait()

		mu.Lock()
		defer mu.Unlock()

		assert.Equal(t, iterations, createdCount)
		assert.Equal(t, iterations, startedCount)
		assert.Equal(t, iterations, doneCount)
		assert.Equal(t, iterations, failedCount)
		assert.Equal(t, iterations, pendingCount)
		assert.Equal(t, iterations, canceledCount)
		assert.Equal(t, iterations, taskFnCount)
	})
}

// Benchmark tests for hooks
func BenchmarkStateHooks_Creation(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewStateHooks(
			WhenCreated(func(id uint64, when time.Time) {}),
			WhenStarted(func(id uint64, when time.Time) {}),
			WhenDone(func(id uint64, when time.Time) {}),
		)
	}
}

func BenchmarkStateHooks_CallEmpty(b *testing.B) {
	hooks := NewStateHooks()
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hooks.onCreated(1, now)
	}
}

func BenchmarkStateHooks_CallWithFunction(b *testing.B) {
	hooks := NewStateHooks(
		WhenCreated(func(id uint64, when time.Time) {}),
	)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hooks.onCreated(1, now)
	}
}

func BenchmarkStateHooks_PanicRecovery(b *testing.B) {
	hooks := NewStateHooks(
		WhenCreated(func(id uint64, when time.Time) {
			panic("benchmark panic")
		}),
	)
	now := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hooks.onCreated(1, now)
	}
}
