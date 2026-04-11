# go-asynctasklib

[![Go Version](https://img.shields.io/badge/go-1.26.1+-blue.svg)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Coverage](https://img.shields.io/badge/coverage-96%25-brightgreen.svg)](https://github.com/barnowlsnest/go-asynctasklib)

Simple Go library for managing asynchronous tasks with context-aware execution, automatic retries, state hooks, and worker pool orchestration.

## Features

### Task Package (`pkg/task`)

- **Context-Aware Execution**: First-class support for `context.Context` with cancellation and timeout handling
- **Automatic Retries**: Configurable retry logic with attempt tracking and pluggable retry strategies
- **State Hooks**: Event-driven callbacks for task lifecycle events (created, started, done, failed, canceled)
- **Thread-Safe**: Built with `sync/atomic` and proper synchronization primitives
- **Graceful Error Handling**: Structured error types with panic recovery
- **Comprehensive State Management**: Track tasks through their entire lifecycle
- **Builder Pattern**: Fluent API for constructing task definitions with validation

### TaskGroup Package (`pkg/taskgroup`)

- **TaskGroup**: Manage concurrent task execution with configurable worker limits
- **ErrorGroup Integration**: Coordinated error handling using `golang.org/x/sync/errgroup`
- **Batch Operations**: Submit multiple tasks efficiently
- **Pool Statistics**: Real-time monitoring of task states and worker utilization
- **Graceful Shutdown**: Context-aware stopping with proper cleanup

### Retry Package (`pkg/retry`)

- **Linear Strategy**: Linearly increasing delay (`baseDelay * attempt`)
- **Exponential Backoff**: Exponentially increasing delay (`baseDelay * 2^attempt`) with overflow protection
- **Jitter**: Optional randomization to prevent thundering herd
- **Configurable**: Base delay, max delay cap, and jitter via functional options
- **Constant delay** is handled natively via `Definition.RetryDelay` — no strategy needed

### Yielder Package (`pkg/yielder`)

- **Generic Yielder**: One-shot, generic value generator with channel-based output
- **Context-Aware**: Respects context cancellation during value emission
- **Timeout Protection**: Configurable timeout watchdog stops idle yielders
- **Panic Recovery**: Generator function panics are recovered and surfaced as errors
- **Error Joining**: Multiple errors are accumulated via `errors.Join`
- **Functional Options**: `WithTimeout`, `WithBuffer`, `WithGeneratorFunc`, `WithValues`

### Semaphore Package (`pkg/semaphore`)

- **Semaphore**: Lock-free, channel-based semaphore with multiple acquisition modes for custom concurrency control

### WorkerPool Package (`pkg/workerpool`)

- **Two Modes**: `FixedSize` pre-subscribes all workers on startup; `AutoScale` keeps `MinSize` workers warm and Joins more (up to `MaxSize`) under load, Leaving idle workers after `IdleTimeout`
- **Claims Dispatcher**: Workers advertise availability on a shared buffered channel — no per-submit scan, no queue-per-worker fan-out
- **Generic over Job Type**: `WorkerPool[T]` — no `interface{}`, no boxing, handlers type-checked at compile time
- **Panic Recovery**: Handler panics are caught, surfaced as `ErrWorkerPanic` via `Events.JobFailed`, and the worker keeps serving
- **Rate Limiting**: Token-bucket limiter (`golang.org/x/time/rate`) caps submissions per second into the backlog
- **Pluggable Events**: `Events[T]` interface observes every lifecycle transition (worker start/stop, subscribe, job ok/failed, leave timeout); embed `NoopEvents[T]` to override only the hooks you care about
- **Graceful Shutdown**: `Close()` is idempotent via `sync.Once`, cancels in-flight work via context, and rejects post-close submissions with `ErrPoolShutdown`

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

### Using the Builder Pattern

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

func main() {
    // Build a task definition with validation
    def, err := task.NewBuilder(
        task.WithID(1),
        task.WithName("validated-task"),
        task.WithTaskFn(func(r *task.Run) error {
            fmt.Printf("Running task %d\n", r.ID())
            return nil
        }),
        task.WithMaxRetries(3),
        task.WithTimeout(30*time.Second),
    ).Build()
    if err != nil {
        panic(err) // Handles ErrIDNotSet, ErrTaskFnNotSet, ErrMaxRetriesNotSet
    }

    t := task.New(*def)
    t.Go(context.Background())
    t.Await()
}
```

### Retry Strategies

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/retry"
    "github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

func main() {
    // Constant delay: just set RetryDelay on the definition — no strategy needed
    t := task.New(task.Definition{
        ID:         1,
        Name:       "simple-retry",
        MaxRetries: 3,
        RetryDelay: 200 * time.Millisecond, // Fixed 200ms between retries
        TaskFn: func(r *task.Run) error {
            return errors.New("temporary error")
        },
    })

    ctx := context.Background()
    if err := t.GoRetry(ctx); err != nil {
        fmt.Printf("All retries exhausted: %v\n", err)
    }

    // Exponential backoff: 200ms, 400ms, 800ms, ... capped at 5s
    t2 := task.New(task.Definition{
        ID:         2,
        Name:       "api-call",
        MaxRetries: 5,
        RetryStrategy: retry.NewExponentialBackoff(
            retry.WithBaseDelay(200*time.Millisecond),
            retry.WithMaxDelay(5*time.Second),
            retry.WithJitter(true), // Randomize to avoid thundering herd
        ),
        TaskFn: func(r *task.Run) error {
            return errors.New("rate limited")
        },
    })

    if err := t2.GoRetry(ctx); err != nil {
        fmt.Printf("All retries exhausted: %v\n", err)
    }

    // Other strategies:
    // retry.NewLinear()              - linearly increasing delay
    // retry.NewExponentialBackoff()  - exponentially increasing delay
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

### Yielder

```go
package main

import (
    "context"
    "fmt"

    "github.com/barnowlsnest/go-asynctasklib/pkg/yielder"
)

func main() {
    // From static values
    y, err := yielder.New[string](context.Background(),
        yielder.WithValues([]string{"alpha", "beta", "gamma"}),
    )
    if err != nil {
        panic(err)
    }

    for v := range y.Results() {
        fmt.Println(v)
    }

    if err := y.Err(); err != nil {
        fmt.Printf("Yielder error: %v\n", err)
    }
}
```

```go
// From a generator function with custom timeout and buffer
y, err := yielder.New[int](ctx,
    yielder.WithGeneratorFunc(func() ([]int, error) {
        // fetch or compute values
        return []int{1, 2, 3}, nil
    }),
    yielder.WithTimeout[int](5*time.Second),
    yielder.WithBuffer[int](10),
)
if err != nil {
    panic(err)
}

// Consume results — channel closes when generation completes
for v := range y.Results() {
    process(v)
}

// Or wait for completion via Done channel
<-y.Done()
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

### Worker Pool — Fixed Size

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/barnowlsnest/go-asynctasklib/pkg/workerpool"
)

func main() {
    handler := func(_ context.Context, job *int) error {
        fmt.Printf("processing %d\n", *job)
        return nil
    }

    pool, err := workerpool.New(context.Background(),
        workerpool.WithConfig[int](&workerpool.Config{
            Mode: workerpool.ModeFixedSize,
            ClaimsConfig: workerpool.ClaimsConfig{
                Size:          8,
                SubmitTimeout: 2 * time.Second,
            },
            Backlog:   100,
            RateLimit: 1000, // max submissions per second
        }),
        workerpool.WithHandler(handler),
    )
    if err != nil {
        panic(err)
    }
    defer pool.Close()

    for i := 1; i <= 20; i++ {
        job := i
        if err := pool.Submit(&job); err != nil {
            fmt.Printf("submit %d: %v\n", i, err)
        }
    }
}
```

### Worker Pool — Auto-Scaling

`AutoScale` keeps a warm minimum of workers subscribed and adds more under
load (up to `MaxSize`). Workers that idle beyond `IdleTimeout` are removed
and re-added the next time the backlog grows. Use `pool.JoinedCount()` to
observe the current number of subscribed workers.

```go
pool, err := workerpool.New(ctx,
    workerpool.WithConfig[int](&workerpool.Config{
        Mode:          workerpool.ModeAutoScale,
        MinSize:       2,
        MaxSize:       16,
        IdleTimeout:   5 * time.Second,
        ScaleInterval: 100 * time.Millisecond,
        ClaimsConfig: workerpool.ClaimsConfig{
            SubmitTimeout: 3 * time.Second,
        },
        Backlog:   500,
        RateLimit: 2000,
    }),
    workerpool.WithHandler(handler),
)
if err != nil {
    panic(err)
}
defer pool.Close()
```

### Worker Pool — Observing Events

Pass an `Events[T]` implementation to observe worker lifecycle and job
outcomes. Embed `NoopEvents[T]` to override only the hooks you care about.

```go
type metrics struct {
    workerpool.NoopEvents[int]
    ok   atomic.Int64
    fail atomic.Int64
}

func (m *metrics) JobOk(_ *int)              { m.ok.Add(1) }
func (m *metrics) JobFailed(_ error, _ *int) { m.fail.Add(1) }

m := &metrics{}
pool, err := workerpool.New(ctx,
    workerpool.WithConfig[int](cfg),
    workerpool.WithHandler(handler),
    workerpool.WithEvents[int](m),
)
```

## API Reference

### Task Definition

```go
type RunFunc func(*Run) error

type Definition struct {
    ID            uint64              // Unique task identifier
    Name          string              // Human-readable task name
    TaskFn        RunFunc             // Task function to execute
    Hooks         *StateHooks         // State change callbacks
    Delay         time.Duration       // Delay before starting
    MaxDuration   time.Duration       // Execution timeout (default: 30s)
    RetryDelay    time.Duration       // Constant delay between retries (used when RetryStrategy is nil)
    MaxRetries    int                 // Number of retry attempts (0 = no retries)
    RetryStrategy retry.Strategy      // Retry delay strategy (nil = use RetryDelay)
}
```

### Builder Pattern

Create task definitions with validation using the fluent builder API:

```go
def, err := task.NewBuilder(opts...).Build()
```

**Builder Options:**
- `WithID(id uint64)` - Set task ID (required)
- `WithName(name string)` - Set task name (defaults to "task-{ID}" if not set)
- `WithTaskFn(fn RunFunc)` - Set task function (required)
- `WithMaxRetries(retries int)` - Set max retries (required, must be > 0)
- `WithDelay(delay time.Duration)` - Set delay before starting
- `WithTimeout(duration time.Duration)` - Set execution timeout
- `WithHooks(hooks *StateHooks)` - Set state change callbacks
- `WithRetryDelay(delay time.Duration)` - Set constant delay between retries
- `WithRetryStrategy(strategy retry.Strategy)` - Set retry delay strategy (overrides RetryDelay)

**Build Errors:**
- `ErrIDNotSet` - ID is 0
- `ErrTaskFnNotSet` - TaskFn is nil
- `ErrMaxRetriesNotSet` - MaxRetries is <= 0

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
- `Stop()` - Stop accepting new tasks
- `Stats() Stats` - Get pool statistics
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

### Retry Strategies (`pkg/retry`)

**Constant delay** is built into `Definition.RetryDelay` — no strategy object needed.

**Strategies** (for variable delay patterns):
- `retry.NewLinear(opts ...OptionFunc) *Linear` - Delay increases as `baseDelay * (attempt + 1)`
- `retry.NewExponentialBackoff(opts ...OptionFunc) *ExponentialBackoff` - Delay increases as `baseDelay * 2^attempt`

**Options:**
- `retry.WithBaseDelay(d time.Duration)` - Base delay between retries (default: 100ms)
- `retry.WithMaxDelay(d time.Duration)` - Maximum delay cap (default: 30s)
- `retry.WithJitter(bool)` - Randomize delay in [0, calculatedDelay] (default: false)

### Yielder (`pkg/yielder`)

- `yielder.New[T](ctx, opts ...Option[T]) (*Yielder[T], error)` - Create and start a new yielder
- `Results() <-chan T` - Claims of generated values (closes on completion)
- `Done() <-chan struct{}` - Closed when the yielder finishes (success, error, timeout, or stop)
- `Stop()` - Stop the yielder (idempotent)
- `Err() error` - Get accumulated errors

**Options:**
- `WithGeneratorFunc[T](fn func() ([]T, error))` - Set the generator function
- `WithValues[T](values []T)` - Use a static slice as the generator
- `WithTimeout[T](d time.Duration)` - Timeout before auto-stop (default: 1s)
- `WithBuffer[T](size int)` - Result channel buffer size (default: 1)

**Errors:**
- `ErrNil` - Generator function is nil
- `ErrTimeout` - Yielder timed out
- `ErrStopped` - Yielder was stopped during emission

### Semaphore Methods (`pkg/semaphore`)

- `semaphore.NewSemaphore(limit int) *Semaphore` - Create a new semaphore
- `Acquire()` - Blocking acquire
- `TryAcquire() bool` - Non-blocking acquire
- `AcquireWithContext(ctx) error` - Context-aware acquire
- `TryAcquireWithTimeout(duration) bool` - Timeout-based acquire
- `Release()` - Release a slot
- `Limit()`, `Available()`, `Acquired()` - Query semaphore state

### WorkerPool (`pkg/workerpool`)

**Constructor:**

- `workerpool.New[T](ctx, opts ...PoolOptionFunc[T]) (*WorkerPool[T], error)` — construct a pool; requires `WithConfig` and `WithHandler`

**Options:**

- `WithConfig[T](cfg *Config)` — pool configuration (mode, sizes, backlog, rate limit)
- `WithHandler[T](fn HandlerFunc[T])` — job handler `func(ctx context.Context, job *T) error`
- `WithEvents[T](events Events[T])` — lifecycle observer (defaults to `NoopEvents[T]`)

**Pool Methods:**

- `Submit(job *T) error` — enqueue a job into the backlog; convenience wrapper around `SubmitContext` with a background context
- `SubmitContext(ctx context.Context, job *T) error` — enqueue a job honoring both the caller's `ctx` and the pool's lifecycle context. Returns `ctx.Err()` if the caller cancels first, `ErrPoolShutdown` if the pool cancels first (pool wins on ties), `ErrSubmitTimeout` if the backlog stays full past `SubmitTimeout`
- `Close()` — idempotent shutdown; cancels in-flight work and rejects new submissions
- `JoinedCount() int` — number of workers currently subscribed to the Claims dispatcher
- `Err() error` — last fatal dispatch error, if any

**Config:**

```go
type Config struct {
    ClaimsConfig                 // Size, SubmitTimeout, SubmitBackoff, Name
    Mode          Mode           // ModeFixedSize or ModeAutoScale
    MinSize       int            // AutoScale: minimum joined workers (warm pool)
    MaxSize       int            // AutoScale: upper bound on joined workers
    IdleTimeout   time.Duration  // AutoScale: leave threshold (default 5s)
    ScaleInterval time.Duration  // AutoScale: scaler period (default 100ms)
    RateLimit     float64        // max Submit rate per second (default 750)
    Backlog       int            // buffered job queue size (default 1000)
}
```

**Errors:**

- `ErrInvalidPool` — construction failure (missing handler/config, nil or canceled context)
- `ErrPoolShutdown` — `Submit` / `SubmitContext` called after `Close`, or the pool's lifecycle context was canceled while waiting
- `ErrNilCtx` — `SubmitContext(nil, ...)`
- `ErrNilJob` — `Submit(nil)` or `SubmitContext(ctx, nil)`
- `ErrSubmitTimeout` — backlog full for longer than `SubmitTimeout`
- `ErrNoWorkers` — Claims dispatcher has no subscribers (AutoScale nudges the scaler and retries)
- `ErrWorkerPanic` — handler panic caught by the worker, surfaced via `Events.JobFailed`
- `ErrDispatcherClosed` — Claims channel closed while `Submit` was in progress
- `ErrMaxPoolSize` — Subscribe called when Claims is at `Size` capacity

**Events[T] interface:**

```go
type Events[T any] interface {
    WorkerStarted(id uint64)
    WorkerStopped(id uint64)
    Subscribed(id uint64)
    SubscribeFailed(err error, id uint64)
    Unsubscribed(id uint64)
    UnsubscribeFailed(err error, id uint64)
    LeaveTimeout(id uint64, timeout time.Duration)
    JobOk(job *T)
    JobFailed(err error, job *T)
}
```

`NoopEvents[T]` implements every method as a no-op so custom observers can
embed it and override only the hooks they care about.

## Architecture

### Synchronization Patterns

The library uses several synchronization primitives:

- **`sync/atomic`**: For thread-safe state management and counters
- **`sync.Mutex`**: To protect shared data structures (task lists, slices)
- **`sync.WaitGroup`**: For coordinating goroutine completion and cleanup
- **Claims-based Semaphore**: Lock-free concurrency control

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
