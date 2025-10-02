package workflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSemaphore(t *testing.T) {
	t.Run("creates semaphore with correct limit", func(t *testing.T) {
		s := NewSemaphore(5)
		require.NotNil(t, s)
		assert.Equal(t, 5, s.Limit())
		assert.Equal(t, 5, s.Available())
		assert.Equal(t, 0, s.Acquired())
	})

	t.Run("creates semaphore with limit 1", func(t *testing.T) {
		s := NewSemaphore(1)
		require.NotNil(t, s)
		assert.Equal(t, 1, s.Limit())
	})

	t.Run("creates semaphore with large limit", func(t *testing.T) {
		s := NewSemaphore(1000)
		require.NotNil(t, s)
		assert.Equal(t, 1000, s.Limit())
	})
}

func TestSemaphore_Acquire_Release(t *testing.T) {
	t.Run("acquires and releases successfully", func(t *testing.T) {
		s := NewSemaphore(3)

		s.Acquire()
		assert.Equal(t, 1, s.Acquired())
		assert.Equal(t, 2, s.Available())

		s.Acquire()
		assert.Equal(t, 2, s.Acquired())
		assert.Equal(t, 1, s.Available())

		s.Release()
		assert.Equal(t, 1, s.Acquired())
		assert.Equal(t, 2, s.Available())

		s.Release()
		assert.Equal(t, 0, s.Acquired())
		assert.Equal(t, 3, s.Available())
	})

	t.Run("blocks when limit reached", func(t *testing.T) {
		s := NewSemaphore(2)

		// Fill the semaphore
		s.Acquire()
		s.Acquire()
		assert.Equal(t, 2, s.Acquired())
		assert.Equal(t, 0, s.Available())

		// Try to acquire in goroutine - should block
		acquired := make(chan bool, 1)
		go func() {
			s.Acquire()
			acquired <- true
		}()

		// Should not acquire immediately
		select {
		case <-acquired:
			t.Fatal("Should not have acquired - semaphore is full")
		case <-time.After(50 * time.Millisecond):
			// Expected - goroutine is blocked
		}

		// Release one slot
		s.Release()

		// Now the blocked goroutine should acquire
		select {
		case <-acquired:
			// Expected
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Goroutine should have acquired after release")
		}

		// Clean up
		s.Release()
		s.Release()
	})
}

func TestSemaphore_TryAcquire(t *testing.T) {
	t.Run("succeeds when slots available", func(t *testing.T) {
		s := NewSemaphore(2)

		assert.True(t, s.TryAcquire())
		assert.Equal(t, 1, s.Acquired())

		assert.True(t, s.TryAcquire())
		assert.Equal(t, 2, s.Acquired())

		s.Release()
		s.Release()
	})

	t.Run("fails when no slots available", func(t *testing.T) {
		s := NewSemaphore(1)

		assert.True(t, s.TryAcquire())
		assert.False(t, s.TryAcquire())
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})

	t.Run("does not block", func(t *testing.T) {
		s := NewSemaphore(0) // No capacity

		done := make(chan bool, 1)
		go func() {
			result := s.TryAcquire()
			assert.False(t, result)
			done <- true
		}()

		select {
		case <-done:
			// Expected - should return immediately
		case <-time.After(50 * time.Millisecond):
			t.Fatal("TryAcquire should not block")
		}
	})
}

func TestSemaphore_AcquireWithContext(t *testing.T) {
	t.Run("acquires successfully when slots available", func(t *testing.T) {
		s := NewSemaphore(2)
		ctx := context.Background()

		err := s.AcquireWithContext(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire() // Fill the semaphore

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := s.AcquireWithContext(ctx)
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
		assert.Equal(t, 1, s.Acquired()) // Should not have acquired

		s.Release()
	})

	t.Run("cancels while waiting", func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire() // Fill the semaphore

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := s.AcquireWithContext(ctx)
		elapsed := time.Since(start)

		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, err)
		assert.Less(t, elapsed, 100*time.Millisecond)
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})

	t.Run("acquires when slot becomes available before context expires", func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire() // Fill the semaphore

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Release after 50ms
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.Release()
		}()

		err := s.AcquireWithContext(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})
}

func TestSemaphore_TryAcquireWithTimeout(t *testing.T) {
	t.Run("succeeds immediately when slots available", func(t *testing.T) {
		s := NewSemaphore(2)

		assert.True(t, s.TryAcquireWithTimeout(100*time.Millisecond))
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})

	t.Run("times out when no slots available", func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire() // Fill the semaphore

		start := time.Now()
		result := s.TryAcquireWithTimeout(50 * time.Millisecond)
		elapsed := time.Since(start)

		assert.False(t, result)
		assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
		assert.Less(t, elapsed, 100*time.Millisecond)
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})

	t.Run("succeeds when slot becomes available before timeout", func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire() // Fill the semaphore

		// Release after 50ms
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.Release()
		}()

		result := s.TryAcquireWithTimeout(200 * time.Millisecond)
		assert.True(t, result)
		assert.Equal(t, 1, s.Acquired())

		s.Release()
	})
}

func TestSemaphore_QueryMethods(t *testing.T) {
	t.Run("Limit returns capacity", func(t *testing.T) {
		s := NewSemaphore(10)
		assert.Equal(t, 10, s.Limit())
	})

	t.Run("Available and Acquired are consistent", func(t *testing.T) {
		s := NewSemaphore(5)

		// Initially all available
		assert.Equal(t, 5, s.Available())
		assert.Equal(t, 0, s.Acquired())
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())

		// Acquire some
		s.Acquire()
		s.Acquire()
		assert.Equal(t, 3, s.Available())
		assert.Equal(t, 2, s.Acquired())
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())

		// Acquire more
		s.Acquire()
		assert.Equal(t, 2, s.Available())
		assert.Equal(t, 3, s.Acquired())
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())

		// Release some
		s.Release()
		s.Release()
		assert.Equal(t, 4, s.Available())
		assert.Equal(t, 1, s.Acquired())
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())

		// Release all
		s.Release()
		assert.Equal(t, 5, s.Available())
		assert.Equal(t, 0, s.Acquired())
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())
	})
}

func TestSemaphore_BinarySemaphore(t *testing.T) {
	t.Run("behaves like mutex with limit 1", func(t *testing.T) {
		s := NewSemaphore(1)
		counter := 0
		iterations := 100

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations/10; j++ {
					s.Acquire()
					// Critical section
					temp := counter
					time.Sleep(time.Microsecond)
					counter = temp + 1
					s.Release()
				}
			}()
		}

		wg.Wait()
		assert.Equal(t, iterations, counter)
	})
}

func TestSemaphore_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent acquire and release", func(t *testing.T) {
		s := NewSemaphore(10)
		iterations := 100
		goroutines := 20

		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					s.Acquire()
					// Simulate work
					time.Sleep(time.Microsecond)
					s.Release()
				}
			}()
		}

		wg.Wait()
		assert.Equal(t, 0, s.Acquired())
		assert.Equal(t, 10, s.Available())
	})

	t.Run("never exceeds limit under concurrent load", func(t *testing.T) {
		limit := 5
		s := NewSemaphore(limit)
		var maxAcquired atomic.Int32
		violations := make(chan int, 100)

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					s.Acquire()

					// Check if limit exceeded
					current := s.Acquired()
					if current > limit {
						violations <- current
					}

					// Update max
					for {
						old := maxAcquired.Load()
						if current <= int(old) || maxAcquired.CompareAndSwap(old, int32(current)) {
							break
						}
					}

					time.Sleep(time.Microsecond)
					s.Release()
				}
			}()
		}

		wg.Wait()
		close(violations)

		// Check for violations
		violationCount := 0
		for v := range violations {
			t.Errorf("Limit exceeded: %d acquired with limit %d", v, limit)
			violationCount++
		}
		assert.Equal(t, 0, violationCount)
		assert.LessOrEqual(t, int(maxAcquired.Load()), limit)
	})

	t.Run("concurrent TryAcquire", func(t *testing.T) {
		s := NewSemaphore(5)
		successCount := atomic.Int32{}

		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if s.TryAcquire() {
					successCount.Add(1)
					time.Sleep(10 * time.Millisecond)
					s.Release()
				}
			}()
		}

		wg.Wait()
		assert.Equal(t, 0, s.Acquired())
		assert.Greater(t, int(successCount.Load()), 0)
	})

	t.Run("concurrent query methods", func(t *testing.T) {
		s := NewSemaphore(10)

		var wg sync.WaitGroup
		// Readers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					limit := s.Limit()
					available := s.Available()
					acquired := s.Acquired()

					// The invariant should hold, but we need to account for
					// race conditions where values change between reads
					sum := available + acquired
					assert.GreaterOrEqual(t, sum, limit-10, "sum should be close to limit")
					assert.LessOrEqual(t, sum, limit+10, "sum should be close to limit")
				}
			}()
		}

		// Writers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					s.Acquire()
					time.Sleep(time.Microsecond)
					s.Release()
				}
			}()
		}

		wg.Wait()

		// After all operations complete, the invariant must hold exactly
		assert.Equal(t, s.Limit(), s.Available()+s.Acquired())
	})

	t.Run("stress test with mixed operations", func(t *testing.T) {
		s := NewSemaphore(20)
		duration := 100 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), duration)
		defer cancel()

		var wg sync.WaitGroup

		// Blocking acquires
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						s.Acquire()
						time.Sleep(time.Microsecond)
						s.Release()
					}
				}
			}()
		}

		// TryAcquire
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						if s.TryAcquire() {
							time.Sleep(time.Microsecond)
							s.Release()
						}
					}
				}
			}()
		}

		// Context-based acquires
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
						if err := s.AcquireWithContext(acquireCtx); err == nil {
							time.Sleep(time.Microsecond)
							s.Release()
						}
						acquireCancel()
					}
				}
			}()
		}

		wg.Wait()
		assert.Equal(t, 0, s.Acquired())
	})
}

func TestSemaphore_WorkerPool(t *testing.T) {
	t.Run("simulates worker pool with limited concurrency", func(t *testing.T) {
		maxWorkers := 5
		s := NewSemaphore(maxWorkers)
		taskCount := 50
		completedTasks := atomic.Int32{}
		var maxConcurrent atomic.Int32

		var wg sync.WaitGroup
		for i := 0; i < taskCount; i++ {
			wg.Add(1)
			go func(taskID int) {
				defer wg.Done()

				s.Acquire()
				defer s.Release()

				// Track max concurrent workers
				current := int32(s.Acquired())
				for {
					old := maxConcurrent.Load()
					if current <= old || maxConcurrent.CompareAndSwap(old, current) {
						break
					}
				}

				// Simulate work
				time.Sleep(time.Millisecond)
				completedTasks.Add(1)
			}(i)
		}

		wg.Wait()

		assert.Equal(t, int32(taskCount), completedTasks.Load())
		assert.LessOrEqual(t, int(maxConcurrent.Load()), maxWorkers)
		assert.Equal(t, 0, s.Acquired())
	})
}

func TestSemaphore_RateLimiting(t *testing.T) {
	t.Run("limits rate of operations", func(t *testing.T) {
		rateLimit := 10
		s := NewSemaphore(rateLimit)
		operations := 30
		completedOps := atomic.Int32{}

		start := time.Now()

		var wg sync.WaitGroup
		for i := 0; i < operations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				s.Acquire()
				completedOps.Add(1)

				// Release after fixed interval to simulate rate limiting
				go func() {
					time.Sleep(50 * time.Millisecond)
					s.Release()
				}()
			}()
		}

		wg.Wait()
		elapsed := time.Since(start)

		assert.Equal(t, int32(operations), completedOps.Load())

		// With rate limit of 10 and 30 operations, taking 50ms each batch,
		// we expect at least 100ms (3 batches: 0-50ms, 50-100ms, 100-150ms)
		assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)

		// Wait for all releases
		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, 0, s.Acquired())
	})
}

// Benchmark tests
func BenchmarkSemaphore_Acquire_Release(b *testing.B) {
	s := NewSemaphore(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Acquire()
		s.Release()
	}
}

func BenchmarkSemaphore_TryAcquire(b *testing.B) {
	s := NewSemaphore(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.TryAcquire() {
			s.Release()
		}
	}
}

func BenchmarkSemaphore_AcquireWithContext(b *testing.B) {
	s := NewSemaphore(100)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.AcquireWithContext(ctx)
		s.Release()
	}
}

func BenchmarkSemaphore_QueryMethods(b *testing.B) {
	s := NewSemaphore(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Limit()
		_ = s.Available()
		_ = s.Acquired()
	}
}

func BenchmarkSemaphore_ConcurrentContention(b *testing.B) {
	s := NewSemaphore(10)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Acquire()
			s.Release()
		}
	})
}
