# go-asynctasklib

[![Go Version](https://img.shields.io/badge/go-1.24.5+-blue.svg)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Coverage](https://img.shields.io/badge/coverage-96%25-brightgreen.svg)](https://github.com/barnowlsnest/go-asynctasklib)

A production-ready Go library for managing asynchronous tasks with context-aware execution, automatic retries, state hooks, and worker pool orchestration.

## Features

### Task Package (`pkg/task`)

- **Context-Aware Execution**: First-class support for `context.Context` with cancellation and timeout handling
- **Automatic Retries**: Configurable retry logic with exponential backoff
- **State Hooks**: Event-driven callbacks for task lifecycle events (created, started, done, failed, canceled)
- **Thread-Safe**: Built with `sync/atomic` and proper synchronization primitives
- **Graceful Error Handling**: Structured error types with panic recovery
- **Comprehensive State Management**: Track tasks through their entire lifecycle

### Workflow Package (`pkg/workflow`)

- **Worker Pool**: Manage concurrent task execution with configurable worker limits
- **Semaphore-Based Concurrency Control**: Lock-free, channel-based semaphore with multiple acquisition modes
- **ErrorGroup Integration**: Coordinated error handling using `golang.org/x/sync/errgroup`
- **Batch Operations**: Submit multiple tasks efficiently
- **Pool Statistics**: Real-time monitoring of task states and worker utilization
- **Graceful Shutdown**: Context-aware stopping with proper cleanup

## Installation

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

### Worker Pool

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
    // Create a worker pool with 10 concurrent workers
    pool := workflow.NewWorkerPool(10)
    defer pool.Wait() // Ensure all tasks complete

    ctx := context.Background()

    // Submit tasks
    for i := 0; i < 100; i++ {
        id := i
        _, err := pool.Submit(ctx, task.Definition{
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
    pool.Wait()

    // Check statistics
    stats := pool.Stats()
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
    pool := workflow.NewWorkerPool(5)

    tasks := []task.Definition{
        {ID: "task-1", TaskFn: func(r *task.Run) error { return nil }},
        {ID: "task-2", TaskFn: func(r *task.Run) error { return nil }},
        {ID: "task-3", TaskFn: func(r *task.Run) error { return fmt.Errorf("simulated error") }},
    }

    ctx := context.Background()

    // First error will cancel all remaining tasks
    if err := pool.SubmitWithErrGroup(ctx, tasks); err != nil {
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
- `Await()` - Block until task completes
- `Cancel()` - Cancel the task
- `State() uint32` - Get current state
- `Err() error` - Get task error
- `IsCreated()`, `IsPending()`, `IsStarted()`, `IsDone()`, `IsFailed()`, `IsCanceled()` - State checks

### Worker Pool Methods

- `Submit(ctx, def) (*Task, error)` - Submit a single task
- `SubmitBatch(ctx, defs) ([]*Task, error)` - Submit multiple tasks
- `SubmitWithErrGroup(ctx, defs) error` - Submit with coordinated error handling
- `Wait()` - Wait for all tasks to complete
- `WaitWithContext(ctx) error` - Context-aware wait
- `Stop()` - Stop accepting new tasks
- `StopWithContext(ctx) error` - Stop and wait with context
- `Stats() PoolStats` - Get pool statistics
- `MaxWorkers()`, `ActiveWorkers()`, `AvailableWorkers()` - Query worker status

### Semaphore Methods

- `Acquire()` - Blocking acquire
- `TryAcquire() bool` - Non-blocking acquire
- `AcquireWithContext(ctx) error` - Context-aware acquire
- `TryAcquireWithTimeout(duration) bool` - Timeout-based acquire
- `Release()` - Release a slot
- `Limit()`, `Available()`, `Acquired()` - Query semaphore state

## Development

### Prerequisites

- Go 1.24.5 or higher
- [Task](https://taskfile.dev) (optional, but recommended)

### Running Tests

```bash
# Using Task
task go-test

# Or directly with go
go test -cover ./...
```

### Code Quality

```bash
# Run all checks (fmt, vet, lint, test)
task sanity

# Individual commands
task go-fmt      # Format code
task go-vet      # Run go vet
task go-lint     # Run golangci-lint
```

## Test Coverage

- **Task Package**: 96% coverage
- **Workflow Package**: 98% coverage

All tests pass with race detection enabled (`-race` flag).

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

## Contributing

Contributions are welcome! Please ensure:

1. All tests pass: `task go-test`
2. Code is formatted: `task go-fmt`
3. No linter issues: `task go-lint`
4. Race detector passes: `go test -race ./...`

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Support

For bugs and feature requests, please open an issue on GitHub.

---

Made with ❤️ by [Barn Owls Nest](https://github.com/barnowlsnest)
