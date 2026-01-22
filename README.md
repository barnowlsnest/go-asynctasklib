# go-asynctasklib

[![Go Version](https://img.shields.io/badge/go-1.24.5+-blue.svg)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Coverage](https://img.shields.io/badge/coverage-96%25-brightgreen.svg)](https://github.com/barnowlsnest/go-asynctasklib)

Simple Go library for managing asynchronous tasks with context-aware execution, automatic retries, state hooks, and worker pool orchestration.

## Features

### Task Package (`pkg/task`)

- **Context-Aware Execution**: First-class support for `context.Context` with cancellation and timeout handling
- **Automatic Retries**: Configurable retry logic with attempt tracking
- **State Hooks**: Event-driven callbacks for task lifecycle events (created, started, done, failed, canceled)
- **Thread-Safe**: Built with `sync/atomic` and proper synchronization primitives
- **Graceful Error Handling**: Structured error types with panic recovery
- **Comprehensive State Management**: Track tasks through their entire lifecycle

### TaskGroup Package (`pkg/taskgroup`)

- **TaskGroup**: Manage concurrent task execution with configurable worker limits
- **ErrorGroup Integration**: Coordinated error handling using `golang.org/x/sync/errgroup`
- **Batch Operations**: Submit multiple tasks efficiently
- **Pool Statistics**: Real-time monitoring of task states and worker utilization
- **Graceful Shutdown**: Context-aware stopping with proper cleanup

### Semaphore Package (`pkg/semaphore`)

- **Semaphore**: Lock-free, channel-based semaphore with multiple acquisition modes for custom concurrency control

## Installation

go 1.25 or later

```bash
go get github.com/barnowlsnest/go-asynctasklib
```

## Quick Start

### Basic Task Execution

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

func main() {
    // Create a task with automatic retries
    t := task.New(task.Definition{
        ID:   1,
        Name: "fetch-user-data",
        TaskFn: func(r *task.Run) error {
            fmt.Printf("Executing task %d (%s)\n", r.ID(), r.Name())
            // Your business logic here
            return nil
        },
        MaxRetries:  3,
        MaxDuration: 30 * time.Second,
    })

    // Start the task
    ctx := context.Background()
    if err := t.Go(ctx); err != nil {
        panic(err)
    }

    // Wait for completion
    t.Await()

    if t.IsFailed() {
        fmt.Printf("Task failed: %v\n", t.Err())
    } else {
        fmt.Println("Task completed successfully")
    }
}
```

### TaskGroup

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
    "github.com/barnowlsnest/go-asynctasklib/pkg/taskgroup"
)

func main() {
    // Create a task group with 10 concurrent workers
    tg, err := taskgroup.New(10)
    if err != nil {
        panic(err)
    }
    defer tg.Wait() // Ensure all tasks complete

    ctx := context.Background()

    // Submit tasks
    for i := 0; i < 100; i++ {
        id := uint64(i)
        _, err := tg.Submit(ctx, task.Definition{
            ID:   id,
            Name: fmt.Sprintf("task-%d", id),
            TaskFn: func(r *task.Run) error {
                fmt.Printf("Processing task %d (%s)\n", r.ID(), r.Name())
                time.Sleep(100 * time.Millisecond)
                return nil
            },
        })
        if err != nil {
            panic(err)
        }
    }

    // Wait for all tasks to complete
    tg.Wait()

    // Check statistics
    stats := tg.Stats()
    fmt.Printf("Completed: %d, Failed: %d\n", stats.Done, stats.Failed)
}
```

### Using ErrorGroup for Coordinated Error Handling

```go
package main

import (
    "context"
    "fmt"

    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
    "github.com/barnowlsnest/go-asynctasklib/pkg/taskgroup"
)

func main() {
    tg, err := taskgroup.New(5)
    if err != nil {
        panic(err)
    }

    tasks := []task.Definition{
        {ID: 1, Name: "task-1", TaskFn: func(r *task.Run) error { return nil }},
        {ID: 2, Name: "task-2", TaskFn: func(r *task.Run) error { return nil }},
        {ID: 3, Name: "task-3", TaskFn: func(r *task.Run) error { return fmt.Errorf("simulated error") }},
    }

    ctx := context.Background()

    // First error will cancel all remaining tasks
    if err := tg.SubmitWithErrGroup(ctx, tasks); err != nil {
        fmt.Printf("Error encountered: %v\n", err)
    }
}
```

### State Hooks

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

func main() {
    hooks := task.NewStateHooks(
        task.WhenStarted(func(id uint64, when time.Time) {
            fmt.Printf("[%s] Task %d started\n", when.Format(time.RFC3339), id)
        }),
        task.WhenDone(func(id uint64, when time.Time) {
            fmt.Printf("[%s] Task %d completed\n", when.Format(time.RFC3339), id)
        }),
        task.WhenFailed(func(id uint64, when time.Time, err error) {
            fmt.Printf("[%s] Task %d failed: %v\n", when.Format(time.RFC3339), id, err)
        }),
    )

    t := task.New(task.Definition{
        ID:     1,
        Name:   "monitored-task",
        Hooks:  hooks,
        TaskFn: func(r *task.Run) error { return nil },
    })

    t.Go(context.Background())
    t.Await()
}
```

## Advanced Usage

### Semaphore for Custom Concurrency Control

```go
package main

import (
    "fmt"
    "sync"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/semaphore"
)

func main() {
    // Create a semaphore with 3 slots
    sem := semaphore.NewSemaphore(3)

    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()

            // Acquire semaphore slot
            sem.Acquire()
            defer sem.Release()

            fmt.Printf("Worker %d processing\n", id)
            time.Sleep(time.Second)
        }(i)
    }

    wg.Wait()
}
```

### Context-Aware Semaphore Acquisition

```go
sem := semaphore.NewSemaphore(5)

ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

// Try to acquire with context
if err := sem.AcquireWithContext(ctx); err != nil {
    fmt.Printf("Failed to acquire: %v\n", err)
    return
}
defer sem.Release()

// Do work...
```

## API Reference

### Task Definition

```go
type Definition struct {
    ID          uint64              // Unique task identifier
    Name        string              // Human-readable task name
    TaskFn      func(*Run) error    // Task function to execute
    Hooks       *StateHooks         // State change callbacks
    Delay       time.Duration       // Delay before starting
    MaxDuration time.Duration       // Execution timeout (default: 30s)
    MaxRetries  int                 // Number of retry attempts (0 = no retries)
}
```

### Task Methods

- `Go(ctx context.Context) error` - Start the task
- `GoRetry(ctx context.Context) error` - Start with automatic retries (requires `MaxRetries > 0`)
- `Await()` - Block until task completes
- `Cancel()` - Cancel the task
- `State() uint32` - Get current state
- `Err() error` - Get task error
- `ID() uint64` - Get task identifier
- `Name() string` - Get task name
- `IsCreated()`, `IsPending()`, `IsStarted()`, `IsDone()`, `IsFailed()`, `IsCanceled()` - State checks
- `IsEnd() bool` - Returns true if task is in DONE, FAILED, or CANCELED state
- `IsInProgress() bool` - Returns true if task is in STARTED or PENDING state

### TaskGroup Methods (`pkg/taskgroup`)

- `taskgroup.New(maxWorkers int) (*TaskGroup, error)` - Create a new task group
- `Submit(ctx, def) (*Task, error)` - Submit a single task
- `SubmitBatch(ctx, defs) ([]*Task, error)` - Submit multiple tasks
- `SubmitWithErrGroup(ctx, defs) error` - Submit with coordinated error handling
- `Wait()` - Wait for all tasks to complete
- `WaitWithContext(ctx) error` - Context-aware wait
- `Stop()` - Stop accepting new tasks
- `StopWithContext(ctx) error` - Stop and cancel in-progress tasks with context
- `Stats() PoolStats` - Get pool statistics
- `Tasks() []*Task` - Get copy of all submitted tasks
- `IsStopped() bool` - Check if task group is stopped
- `MaxWorkers()`, `ActiveWorkers()`, `AvailableWorkers()` - Query worker status

### State Hook Options

- `WhenCreated(fn func(id uint64, when time.Time))` - Called when task is created
- `WhenPending(fn func(id uint64, when time.Time, attempt int))` - Called when task becomes pending (includes retry attempt number)
- `WhenStarted(fn func(id uint64, when time.Time))` - Called when task starts executing
- `WhenDone(fn func(id uint64, when time.Time))` - Called when task completes successfully
- `WhenFailed(fn func(id uint64, when time.Time, err error))` - Called when task fails
- `WhenCanceled(fn func(id uint64, when time.Time))` - Called when task is canceled
- `FromTaskFn(fn func(id uint64, when time.Time))` - Called from within task via `Run.Callback()`

### Semaphore Methods (`pkg/semaphore`)

- `semaphore.NewSemaphore(limit int) *Semaphore` - Create a new semaphore
- `Acquire()` - Blocking acquire
- `TryAcquire() bool` - Non-blocking acquire
- `AcquireWithContext(ctx) error` - Context-aware acquire
- `TryAcquireWithTimeout(duration) bool` - Timeout-based acquire
- `Release()` - Release a slot
- `Limit()`, `Available()`, `Acquired()` - Query semaphore state

## Architecture

### Synchronization Patterns

The library uses several synchronization primitives:

- **`sync/atomic`**: For thread-safe state management and counters
- **`sync.Mutex`**: To protect shared data structures (task lists, slices)
- **`sync.WaitGroup`**: For coordinating goroutine completion and cleanup
- **Channel-based Semaphore**: Lock-free concurrency control

### Design Principles

1. **Context Propagation**: All operations respect context cancellation and timeouts
2. **Fail-Safe Defaults**: Sensible defaults with explicit override options
3. **Memory Safety**: Careful struct field ordering for optimal alignment
4. **Error Transparency**: Structured errors with clear semantics
5. **Zero Dependencies**: Only uses `golang.org/x/sync` for errgroup

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Support

For bugs and feature requests, please open an issue on GitHub.

---

Made with ❤️ by [Barn Owls Nest](https://github.com/barnowlsnest)
