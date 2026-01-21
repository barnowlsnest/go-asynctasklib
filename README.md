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

### Workflow Package (`pkg/workflow`)

- **TaskGroup**: Manage concurrent task execution with configurable worker limits
- **Semaphore-Based Concurrency Control**: Lock-free, channel-based semaphore with multiple acquisition modes
- **ErrorGroup Integration**: Coordinated error handling using `golang.org/x/sync/errgroup`
- **Batch Operations**: Submit multiple tasks efficiently
- **Pool Statistics**: Real-time monitoring of task states and worker utilization
- **Graceful Shutdown**: Context-aware stopping with proper cleanup

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
        ID: "fetch-user-data",
        TaskFn: func(r *task.Run) error {
            fmt.Printf("Executing task %s\n", r.ID())
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
    "github.com/barnowlsnest/go-asynctasklib/pkg/workflow"
)

func main() {
    // Create a task group with 10 concurrent workers
    tg, err := workflow.NewTaskGroup(10)
    if err != nil {
        panic(err)
    }
    defer tg.Wait() // Ensure all tasks complete

    ctx := context.Background()

    // Submit tasks
    for i := 0; i < 100; i++ {
        id := i
        _, err := tg.Submit(ctx, task.Definition{
            ID: fmt.Sprintf("task-%d", id),
            TaskFn: func(r *task.Run) error {
                fmt.Printf("Processing task %s\n", r.ID())
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
    "github.com/barnowlsnest/go-asynctasklib/pkg/workflow"
)

func main() {
    tg, err := workflow.NewTaskGroup(5)
    if err != nil {
        panic(err)
    }

    tasks := []task.Definition{
        {ID: "task-1", TaskFn: func(r *task.Run) error { return nil }},
        {ID: "task-2", TaskFn: func(r *task.Run) error { return nil }},
        {ID: "task-3", TaskFn: func(r *task.Run) error { return fmt.Errorf("simulated error") }},
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
        task.WhenStarted(func(id string, when time.Time) {
            fmt.Printf("[%s] Task %s started\n", when.Format(time.RFC3339), id)
        }),
        task.WhenDone(func(id string, when time.Time) {
            fmt.Printf("[%s] Task %s completed\n", when.Format(time.RFC3339), id)
        }),
        task.WhenFailed(func(id string, when time.Time, err error) {
            fmt.Printf("[%s] Task %s failed: %v\n", when.Format(time.RFC3339), id, err)
        }),
    )

    t := task.New(task.Definition{
        ID:     "monitored-task",
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

    "github.com/barnowlsnest/go-asynctasklib/pkg/workflow"
)

func main() {
    // Create a semaphore with 3 slots
    sem := workflow.NewSemaphore(3)

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
sem := workflow.NewSemaphore(5)

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
    ID          string              // Unique task identifier
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
- `ID() string` - Get task identifier
- `IsCreated()`, `IsPending()`, `IsStarted()`, `IsDone()`, `IsFailed()`, `IsCanceled()` - State checks
- `IsEnd() bool` - Returns true if task is in DONE, FAILED, or CANCELED state
- `IsInProgress() bool` - Returns true if task is in STARTED or PENDING state

### TaskGroup Methods

- `NewTaskGroup(maxWorkers int) (*TaskGroup, error)` - Create a new task group
- `Submit(ctx, def) (*Task, error)` - Submit a single task
- `SubmitBatch(ctx, defs) ([]*Task, error)` - Submit multiple tasks
- `SubmitWithErrGroup(ctx, defs) error` - Submit with coordinated error handling
- `Wait()` - Wait for all tasks to complete
- `WaitWithContext(ctx) error` - Context-aware wait
- `Stop()` - Stop accepting new tasks
- `StopWithContext(ctx) error` - Stop and cancel in-progress tasks with context
- `Stats() PoolStats` - Get pool statistics
- `Tasks() []*Task` - Get copy of all submitted tasks
- `IsStopped() bool` - Check if pool is stopped
- `MaxWorkers()`, `ActiveWorkers()`, `AvailableWorkers()` - Query worker status

### State Hook Options

- `WhenCreated(fn func(id string, when time.Time))` - Called when task is created
- `WhenPending(fn func(id string, when time.Time, attempt int))` - Called when task becomes pending (includes retry attempt number)
- `WhenStarted(fn func(id string, when time.Time))` - Called when task starts executing
- `WhenDone(fn func(id string, when time.Time))` - Called when task completes successfully
- `WhenFailed(fn func(id string, when time.Time, err error))` - Called when task fails
- `WhenCanceled(fn func(id string, when time.Time))` - Called when task is canceled
- `FromTaskFn(fn func(id string, when time.Time))` - Called from within task via `Run.Callback()`

### Semaphore Methods

- `NewSemaphore(limit int) *Semaphore` - Create a new semaphore
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
