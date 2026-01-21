package taskgroup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

func TestNewTaskGroup(t *testing.T) {
	t.Run("creates pool with specified max workers", func(t *testing.T) {
		tg, err := New(5)
		require.NoError(t, err)
		require.NotNil(t, tg)
		assert.Equal(t, 5, tg.MaxWorkers())
		assert.Equal(t, 5, tg.AvailableWorkers())
		assert.Equal(t, 0, tg.ActiveWorkers())
		assert.False(t, tg.IsStopped())
	})

	t.Run("returns error when maxWorkers is zero", func(t *testing.T) {
		tg, err := New(0)
		assert.Error(t, err)
		assert.Equal(t, ErrTaskGroupMaxWorkers, err)
		assert.Nil(t, tg)
	})

	t.Run("returns error when maxWorkers is negative", func(t *testing.T) {
		tg, err := New(-5)
		assert.Error(t, err)
		assert.Equal(t, ErrTaskGroupMaxWorkers, err)
		assert.Nil(t, tg)
	})
}

func TestTaskGroup_Submit(t *testing.T) {
	t.Run("submits and executes task successfully", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()
		executed := false

		tk, err := tg.Submit(ctx, task.Definition{
			ID: "test-task",
			TaskFn: func(r *task.Run) error {
				executed = true
				return nil
			},
		})

		require.NoError(t, err)
		require.NotNil(t, tk)
		assert.Equal(t, "test-task", tk.ID())

		tk.Await()
		assert.True(t, executed)
		assert.True(t, tk.IsDone())
	})

	t.Run("respects max workers limit", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit 2 tasks that will block
		blocker := make(chan struct{})
		for i := 0; i < 2; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					<-blocker
					return nil
				},
			})
			require.NoError(t, err)
		}

		// Wait for tasks to start
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, 2, tg.ActiveWorkers())
		assert.Equal(t, 0, tg.AvailableWorkers())

		// Third task should block until a worker is available
		submitted := make(chan bool)
		go func() {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					return nil
				},
			})
			require.NoError(t, err)
			submitted <- true
		}()

		// Should not submit immediately
		select {
		case <-submitted:
			t.Fatal("Task should not have been submitted - pool is full")
		case <-time.After(50 * time.Millisecond):
			// Expected
		}

		// Release blockers
		close(blocker)

		// Now third task should submit
		select {
		case <-submitted:
			// Expected
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Task should have been submitted after workers became available")
		}

		tg.Wait()
	})

	t.Run("returns error when pool is stopped", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()
		tg.Stop()

		tk, err := tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return nil
			},
		})

		assert.Error(t, err)
		assert.Equal(t, ErrTaskGroupStopped, err)
		assert.Nil(t, tk)
	})

	t.Run("handles task errors", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()
		expectedErr := errors.New("task error")

		tk, err := tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return expectedErr
			},
		})

		require.NoError(t, err)
		tk.Await()

		assert.True(t, tk.IsFailed())
		assert.Equal(t, expectedErr, tk.Err())
	})
}

func TestTaskGroup_SubmitWithErrGroup(t *testing.T) {
	t.Run("executes all tasks successfully", func(t *testing.T) {
		tg, err := New(3)
		require.NoError(t, err)
		ctx := context.Background()

		var counter atomic.Int32
		defs := make([]task.Definition, 10)
		for i := range defs {
			defs[i] = task.Definition{
				TaskFn: func(r *task.Run) error {
					counter.Add(1)
					time.Sleep(10 * time.Millisecond)
					return nil
				},
			}
		}

		err = tg.SubmitWithErrGroup(ctx, defs)
		require.NoError(t, err)
		assert.Equal(t, int32(10), counter.Load())
	})

	t.Run("returns first error and cancels remaining tasks", func(t *testing.T) {
		tg, err := New(3)
		require.NoError(t, err)
		ctx := context.Background()

		expectedErr := errors.New("task failed")
		var successCount atomic.Int32

		defs := []task.Definition{
			{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					successCount.Add(1)
					return nil
				},
			},
			{
				TaskFn: func(r *task.Run) error {
					return expectedErr
				},
			},
			{
				TaskFn: func(r *task.Run) error {
					time.Sleep(50 * time.Millisecond)
					successCount.Add(1)
					return nil
				},
			},
		}

		err = tg.SubmitWithErrGroup(ctx, defs)
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		defs := make([]task.Definition, 5)
		for i := range defs {
			defs[i] = task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(100 * time.Millisecond)
					return nil
				},
			}
		}

		err = tg.SubmitWithErrGroup(ctx, defs)
		assert.Error(t, err)
	})
}

func TestTaskGroup_SubmitBatch(t *testing.T) {
	t.Run("submits multiple tasks and returns all", func(t *testing.T) {
		tg, err := New(5)
		require.NoError(t, err)
		ctx := context.Background()

		defs := make([]task.Definition, 10)
		for i := range defs {
			defs[i] = task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					return nil
				},
			}
		}

		tasks, err := tg.SubmitBatch(ctx, defs)
		require.NoError(t, err)
		assert.Len(t, tasks, 10)

		for _, tk := range tasks {
			tk.Await()
			assert.True(t, tk.IsDone())
		}
	})

	t.Run("returns error if pool is stopped", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()
		tg.Stop()

		defs := []task.Definition{
			{TaskFn: func(r *task.Run) error { return nil }},
		}

		tasks, err := tg.SubmitBatch(ctx, defs)
		assert.Error(t, err)
		assert.Empty(t, tasks)
	})
}

func TestTaskGroup_Wait(t *testing.T) {
	t.Run("waits for all tasks to complete", func(t *testing.T) {
		tg, err := New(3)
		require.NoError(t, err)
		ctx := context.Background()

		var counter atomic.Int32
		for i := 0; i < 10; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					counter.Add(1)
					return nil
				},
			})
			require.NoError(t, err)
		}

		tg.Wait()
		assert.Equal(t, int32(10), counter.Load())
		assert.Equal(t, 0, tg.ActiveWorkers())
	})
}

func TestTaskGroup_WaitWithContext(t *testing.T) {
	t.Run("waits successfully when tasks complete", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		for i := 0; i < 5; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					return nil
				},
			})
			require.NoError(t, err)
		}

		waitCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		err = tg.WaitWithContext(waitCtx)
		require.NoError(t, err)
	})

	t.Run("returns error when context times out", func(t *testing.T) {
		tg, err := New(1)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit a long-running task
		_, err = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				time.Sleep(200 * time.Millisecond)
				return nil
			},
		})
		require.NoError(t, err)

		waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err = tg.WaitWithContext(waitCtx)
		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, err)
	})
}

func TestTaskGroup_Stop(t *testing.T) {
	t.Run("stops pool and prevents new submissions", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit a task
		_, err = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				time.Sleep(50 * time.Millisecond)
				return nil
			},
		})
		require.NoError(t, err)

		tg.Stop()
		assert.True(t, tg.IsStopped())

		// Try to submit after stop
		_, err = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return nil
			},
		})
		assert.Error(t, err)
		assert.Equal(t, ErrTaskGroupStopped, err)
	})
}

func TestTaskGroup_StopWithContext(t *testing.T) {
	t.Run("stops pool and cancels running tasks", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		var canceledCount atomic.Int32
		for i := 0; i < 3; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					select {
					case <-r.Context().Done():
						canceledCount.Add(1)
						return r.Context().Err()
					case <-time.After(200 * time.Millisecond):
						return nil
					}
				},
			})
			require.NoError(t, err)
		}

		time.Sleep(20 * time.Millisecond) // Let tasks start

		stopCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		err = tg.StopWithContext(stopCtx)
		require.NoError(t, err)
		assert.True(t, tg.IsStopped())
		assert.Greater(t, int(canceledCount.Load()), 0)
	})

	t.Run("cancels running tasks when stopped", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit tasks that check for cancellation
		var canceled atomic.Int32
		for i := 0; i < 3; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					select {
					case <-r.Context().Done():
						canceled.Add(1)
						return r.Context().Err()
					case <-time.After(1 * time.Second):
						return nil
					}
				},
			})
			require.NoError(t, err)
		}

		time.Sleep(20 * time.Millisecond) // Let tasks start

		stopCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		err = tg.StopWithContext(stopCtx)
		require.NoError(t, err)
		assert.True(t, tg.IsStopped())
		// At least some tasks should have been canceled
		assert.Greater(t, int(canceled.Load()), 0)
	})
}

func TestTaskGroup_Tasks(t *testing.T) {
	t.Run("returns copy of all submitted tasks", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		for i := 0; i < 5; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					return nil
				},
			})
			require.NoError(t, err)
		}

		tasks := tg.Tasks()
		assert.Len(t, tasks, 5)

		tg.Wait()
	})
}

func TestTaskGroup_Stats(t *testing.T) {
	t.Run("returns accurate statistics", func(t *testing.T) {
		tg, err := New(2)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit mix of tasks
		blocker := make(chan struct{})

		// Running tasks
		for i := 0; i < 2; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					<-blocker
					return nil
				},
			})
			require.NoError(t, err)
		}

		time.Sleep(50 * time.Millisecond) // Let tasks start

		stats := tg.Stats()
		assert.Equal(t, 2, stats.Total)
		assert.Equal(t, 2, stats.MaxWorkers)
		assert.Equal(t, 2, stats.ActiveWorkers)
		assert.Equal(t, 2, stats.Started)

		close(blocker)
		tg.Wait()

		// Give a moment for semaphore release to complete
		time.Sleep(10 * time.Millisecond)

		stats = tg.Stats()
		assert.Equal(t, 2, stats.Done)
		assert.Equal(t, 0, stats.ActiveWorkers)
	})

	t.Run("tracks failed and canceled tasks", func(t *testing.T) {
		tg, err := New(3)
		require.NoError(t, err)
		ctx := context.Background()

		// Successful task
		_, _ = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return nil
			},
		})

		// Failed task
		_, _ = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return errors.New("failed")
			},
		})

		// Canceled task
		cancelCtx, cancel := context.WithCancel(context.Background())
		tk, _ := tg.Submit(cancelCtx, task.Definition{
			TaskFn: func(r *task.Run) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})
		cancel()
		tk.Await()

		tg.Wait()

		stats := tg.Stats()
		assert.Equal(t, 3, stats.Total)
		assert.Equal(t, 1, stats.Done)
		assert.Equal(t, 1, stats.Failed)
		assert.Equal(t, 1, stats.Canceled)
	})
}

func TestTaskGroup_ConcurrentAccess(t *testing.T) {
	t.Run("handles concurrent submissions safely", func(t *testing.T) {
		tg, err := New(10)
		require.NoError(t, err)
		ctx := context.Background()

		var wg sync.WaitGroup
		submissionCount := 100

		for i := 0; i < submissionCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := tg.Submit(ctx, task.Definition{
					TaskFn: func(r *task.Run) error {
						time.Sleep(time.Millisecond)
						return nil
					},
				})
				assert.NoError(t, err)
			}()
		}

		wg.Wait()
		tg.Wait()

		stats := tg.Stats()
		assert.Equal(t, submissionCount, stats.Total)
		assert.Equal(t, submissionCount, stats.Done)
	})

	t.Run("concurrent queries are safe", func(t *testing.T) {
		tg, err := New(5)
		require.NoError(t, err)
		ctx := context.Background()

		// Submit some tasks
		for i := 0; i < 10; i++ {
			_, _ = tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					time.Sleep(10 * time.Millisecond)
					return nil
				},
			})
		}

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = tg.MaxWorkers()
				_ = tg.ActiveWorkers()
				_ = tg.AvailableWorkers()
				_ = tg.IsStopped()
				_ = tg.Stats()
				_ = tg.Tasks()
			}()
		}

		wg.Wait()
		tg.Wait()
	})
}

func TestTaskGroup_RealWorldScenario(t *testing.T) {
	t.Run("processes batch of HTTP-like requests", func(t *testing.T) {
		tg, err := New(5)
		require.NoError(t, err)
		ctx := context.Background()

		requestCount := 50
		var processedCount atomic.Int32
		var maxConcurrent atomic.Int32

		for i := 0; i < requestCount; i++ {
			_, err := tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					// Track concurrency
					current := int32(tg.ActiveWorkers())
					for {
						old := maxConcurrent.Load()
						if current <= old || maxConcurrent.CompareAndSwap(old, current) {
							break
						}
					}

					// Simulate HTTP request processing
					time.Sleep(10 * time.Millisecond)
					processedCount.Add(1)
					return nil
				},
			})
			require.NoError(t, err)
		}

		tg.Wait()

		assert.Equal(t, int32(requestCount), processedCount.Load())
		assert.LessOrEqual(t, int(maxConcurrent.Load()), 5)

		stats := tg.Stats()
		assert.Equal(t, requestCount, stats.Done)
	})
}

// Benchmark tests
func BenchmarkTaskGroup_Submit(b *testing.B) {
	tg, err := New(100)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				return nil
			},
		})
	}

	tg.Wait()
}

func BenchmarkTaskGroup_Stats(b *testing.B) {
	tg, err := New(10)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	// Submit some tasks
	for i := 0; i < 100; i++ {
		_, _ = tg.Submit(ctx, task.Definition{
			TaskFn: func(r *task.Run) error {
				time.Sleep(time.Millisecond)
				return nil
			},
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tg.Stats()
	}

	tg.Wait()
}

func BenchmarkTaskGroup_ConcurrentSubmit(b *testing.B) {
	tg, err := New(50)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = tg.Submit(ctx, task.Definition{
				TaskFn: func(r *task.Run) error {
					return nil
				},
			})
		}
	})

	tg.Wait()
}
