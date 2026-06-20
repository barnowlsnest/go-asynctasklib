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

- **Fixed-Size Pool**: `ModeFixedSize` pre-subscribes all workers on startup; worker count never changes after `New` returns
- **Claims Dispatcher**: Workers advertise availability on a shared buffered channel — no per-submit scan, no queue-per-worker fan-out
- **Generic over Job Type**: `WorkerPool[T]` — no `interface{}`, no boxing, handlers type-checked at compile time
- **Composed Context**: Handlers receive a `JobAware[T]` that merges the pool's lifecycle context with the caller's `Submit` context; whichever cancels first cancels the handler
- **Panic Recovery**: Handler panics are caught, wrapped in `ErrWorkerPanic`, and surfaced via `Events.JobFailed` — the worker keeps serving
- **Rate Limiting**: Token-bucket limiter (`golang.org/x/time/rate`) caps submissions per second into the backlog
- **Pluggable Events**: `PoolEvents[T]` observes every lifecycle transition (worker start/stop, subscribe, job ok/failed, leave timeout, dispatch error); embed `NoopEvents[T]` to override only the hooks you care about
- **Two Shutdown Modes**: `Shutdown()` cancels in-flight work immediately; `GracefulShutdown()` drains the backlog first. Both are idempotent and reject post-shutdown submissions with `ErrPoolShutdown`

### TaskQueue Package (`pkg/taskqueue`)

- **Backend-Agnostic**: ordering and lease storage live behind a `Backend` interface; the in-memory default is ready to be swapped for a distributed/PostgreSQL backend (`SELECT ... FOR UPDATE SKIP LOCKED`) without coupling the library to any backend
- **Priority or FIFO**: `ModePriority` orders by `Priority` descending with FIFO (`Seq`) tie-breaking; `ModeFIFO` orders strictly by `Seq` — selected by the user
- **At-Least-Once Delivery**: claim/ack/nack lease model with a visibility timeout; a crash between claim and ack lets the lease expire and the task redeliver
- **Attempt Tracking + DLQ**: the queue counts deliveries and auto-routes a task to a pluggable `DeadLetter` once it reaches `WithMaxAttempts` without an ack
- **Background Reaper**: sweeps expired leases every `WithReapInterval`, requeuing or dead-lettering them per the same policy as `Nack`
- **Two Consumer APIs**: blocking `Dequeue` for one-at-a-time consumers, plus a yielder-based `Stream` for pipelines
- **Pluggable Events**: `QueueEvents` observes every transition (enqueue, claim, ack, nack, retry, dead-letter, lease expiry); embed `NoopQueueEvents` to override only the hooks you need
- **Graceful Close**: `Close` stops the reaper and is idempotent; producer/consumer calls after close return `ErrQueueClosed`

## Installation

go 1.26.1 or later

```bash
go get github.com/barnowlsnest/go-asynctasklib/v2
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

            fmt.Printf("worker %d processing\n", id)
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

    "github.com/barnowlsnest/go-asynctasklib/v2/pkg/workerpool"
)

func main() {
    handler := func(ctx workerpool.JobAware[int]) error {
        fmt.Printf("processing %d\n", ctx.Job())
        return nil
    }

    pool, err := workerpool.New[int](context.Background(),
        workerpool.WithConfig[int](&workerpool.Config{
            Mode: workerpool.ModeFixedSize,
            ClaimsConfig: workerpool.ClaimsConfig{
                Size:          8,
                SubmitTimeout: 2 * time.Second,
            },
            Backlog:   100,
            RateLimit: 1000, // max submissions per second
        }),
        workerpool.WithHandler[int](handler),
    )
    if err != nil {
        panic(err)
    }
    defer pool.GracefulShutdown()

    ctx := context.Background()
    for i := 1; i <= 20; i++ {
        if err := pool.Submit(ctx, i); err != nil {
            fmt.Printf("submit %d: %v\n", i, err)
        }
    }
}
```

### Worker Pool — Auto-scaling

`ModeAutoScale` keeps the number of joined workers between `MinSize` and
`MaxSize` based on load. In auto mode `MaxSize` is the ceiling — it sizes the
claims buffer, the subscriber cap, and the rate-limiter burst — and
`ClaimsConfig.Size` is ignored.

```go
pool, err := workerpool.New[int](context.Background(),
    workerpool.WithConfig[int](&workerpool.Config{
        Mode: workerpool.ModeAutoScale,
        AutoScale: workerpool.AutoScaleConfig{
            MinSize:             1,
            MaxSize:             8,
            ScaleUpStep:         1,
            Interval:            100 * time.Millisecond,
            IdleHeadroom:        1,
            ScaleDownCooldown:   5 * time.Second,
            ScaleDownIdlePeriod: 2 * time.Second, // tune to >= 2x handler p99
        },
    }),
    workerpool.WithHandler[int](handler),
)
```

The pool scales **up fast** (it may add `ScaleUpStep` workers every `Interval`
while the backlog is non-empty and idle headroom is exhausted) and **down slow**
(at most one worker per `ScaleDownCooldown`, and only workers that have been
idle for at least `ScaleDownIdlePeriod`). It never leaves a worker that is
running a job, and it never drops below `MinSize` except during shutdown. Every
numeric field defaults sensibly when left zero (`MaxSize` → `runtime.NumCPU()`,
`MinSize` → 1).

### Worker Pool — Observing Events

Pass a `PoolEvents[T]` implementation to observe worker lifecycle and job
outcomes. Embed `NoopEvents[T]` to override only the hooks you care about.

```go
type metrics struct {
    workerpool.NoopEvents[int]
    ok   atomic.Int64
    fail atomic.Int64
}

func (m *metrics) JobOk(_ int)              { m.ok.Add(1) }
func (m *metrics) JobFailed(_ error, _ int) { m.fail.Add(1) }

m := &metrics{}
pool, err := workerpool.New[int](ctx,
    workerpool.WithConfig[int](cfg),
    workerpool.WithHandler[int](handler),
    workerpool.WithEvents[int](m),
)
```

### Worker Pool — Per-Submission Context

`Submit` accepts a caller `ctx` that is merged with the pool's lifecycle
context. The handler receives a `JobAware[T]` that satisfies
`context.Context`; whichever parent cancels first cancels the handler.
Values set on the submit context shadow pool-wide values for the same key.

```go
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()

if err := pool.Submit(ctx, payload); err != nil {
    // ErrPoolShutdown, ErrSubmitTimeout, or ctx.Err()
    fmt.Println("submit:", err)
}
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
- `Results() <-chan T` - claims of generated values (closes on completion)
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

- `workerpool.New[T](ctx, opts ...PoolOptionFunc[T]) (*WorkerPool[T], error)` — construct a pool; requires `WithConfig` and `WithHandler`. Returns `ErrInvalidPool` joined with a descriptive error when required options are missing, or `ctx.Err()` if `ctx` is already canceled.

**Options:**

- `WithConfig[T](cfg *Config)` — pool configuration (mode, claims, backlog, rate limit). Required.
- `WithHandler[T](fn HandlerFunc[T])` — job handler `func(JobAware[T]) error`. Required.
- `WithEvents[T](events PoolEvents[T])` — lifecycle observer (defaults to `NoopEvents[T]`)

**Pool Methods:**

- `Submit(ctx context.Context, job T) error` — enqueue a job onto the backlog, passing `ctx` as the caller context. The handler sees a merged context (pool ⊕ caller); whichever cancels first cancels the handler. Returns `ErrNilCtx` if `ctx` is nil, `ErrPoolShutdown` if the pool is shut down, `ctx.Err()` if the pool context cancels while waiting, or `ErrSubmitTimeout` if the backlog stays full past `ClaimsConfig.SubmitTimeout`.
- `Shutdown()` — cancel the pool context immediately; in-flight handlers observe cancellation and jobs still in the backlog are dropped. Idempotent.
- `GracefulShutdown()` — stop accepting new submissions and wait for the dispatcher to hand off every backlogged job before returning. Idempotent.
- `JoinedCount() int` — number of workers currently subscribed to the claims dispatcher
- `Error() error` — last fatal dispatch error, if any

**HandlerFunc and JobAware:**

```go
type HandlerFunc[T any] func(JobAware[T]) error

type JobAware[T any] interface {
    context.Context
    Job() T
}
```

`JobAware[T]` embeds `context.Context`, so handlers honor cancellation and
deadlines with the usual `ctx.Done()` / `ctx.Err()` idioms, and read the
payload via `Job()`.

**Config:**

```go
type Config struct {
    ClaimsConfig          // Size, SubmitTimeout, SubmitBackoff, SubmitAttemptsPerSec, BackoffFactor, Name
    Mode      Mode        // ModeFixedSize (default)
    RateLimit float64     // max Submit rate per second (default 750)
    Backlog   int         // buffered job queue size (default 1000)
}
```

**ClaimsConfig** (embedded into `Config`):

- `Size int` — maximum subscribed workers; also sizes the claims channel (default `runtime.NumCPU()`)
- `SubmitTimeout time.Duration` — max wait for a worker to accept a job (default 1s)
- `SubmitBackoff time.Duration` — initial backoff between retries after a stale claim (default 50ms)
- `SubmitAttemptsPerSec int` — cap on retry rate for stale claims (default 5)
- `BackoffFactor float64` — multiplier on `SubmitBackoff` per retry; must be in (0, 1] (default 1.5 → reset)
- `Name string` — optional label surfaced via the worker debug format

**Errors:**

- `ErrInvalidPool` — construction failure (missing handler/config, nil context)
- `ErrPoolShutdown` — `Submit` called after `Shutdown`/`GracefulShutdown`, or the pool's lifecycle context was canceled while waiting
- `ErrNilCtx` — `Submit(nil, ...)` or a nil context reaching a worker
- `ErrNilJob` — base error wrapped by nil-job conditions
- `ErrNil` — base sentinel joined by every "nil X" error in the package
- `ErrSubmitTimeout` — backlog full, or no worker accepted the job within `SubmitTimeout`
- `ErrNoWorkers` — `SubmitTimeout` elapsed with no subscribed workers
- `ErrWorkerPanic` — handler panic caught by the worker, surfaced via `Events.JobFailed`
- `ErrDispatcherClosed` — claims channel closed while `submit` was in progress
- `ErrMaxPoolSize` — `Subscribe` called when claims is at `Size` capacity
- `ErrInvalidWorker` — worker or worker config is nil
- `ErrWorkerAlreadyRunning` — `Join` called on a worker that is already subscribed
- `ErrWorkerTimeout` — worker did not exit its run loop within the `Leave` timeout

**Events[T] / PoolEvents[T] interfaces:**

```go
type Events[T any] interface {
    WorkerStarted(id uint64)
    WorkerStopped(id uint64)
    Subscribed(id uint64)
    SubscribeFailed(err error, id uint64)
    Unsubscribed(id uint64)
    UnsubscribeFailed(err error, id uint64)
    LeaveTimeout(id uint64, timeout time.Duration)
    JobOk(job T)
    JobFailed(err error, job T)
}

type PoolEvents[T any] interface {
    Events[T]
    DispatchError(err error, job T)
}
```

`DispatchError` fires when the claims dispatcher fails to hand a job to any
worker (typically `ErrSubmitTimeout` with no accepting worker). `NoopEvents[T]`
implements every `PoolEvents[T]` method as a no-op so custom observers can
embed it and override only the hooks they care about.

### TaskQueue (`pkg/taskqueue`)

**Constructor:**

- `taskqueue.New(ctx, opts ...Option) (*Queue, error)` — construct a queue and start its background reaper. Returns `ErrInvalidQueue` (joined with detail) on a nil context, or `ctx.Err()` if `ctx` is already canceled. Defaults to an in-memory backend and dead-letter queue.

**Options:**

- `WithBackend(b Backend)` — custom ordered-storage backend (defaults to in-memory). When set, `WithMode` is ignored — ordering belongs to the backend
- `WithMode(m Mode)` — `ModePriority` or `ModeFIFO` for the default in-memory backend
- `WithDeadLetter(dl DeadLetter)` — custom dead-letter store (defaults to in-memory)
- `WithMaxAttempts(n int)` — deliveries allowed before a task is dead-lettered (default 3)
- `WithVisibilityTimeout(d time.Duration)` — how long a claimed task stays invisible before its lease expires (default 30s)
- `WithReapInterval(d time.Duration)` — how often expired leases are swept (default 5s)
- `WithEvents(e QueueEvents)` — lifecycle observer (defaults to `NoopQueueEvents`)

**Queue Methods:**

- `Enqueue(ctx, task Task) error` — store a task for ordered delivery
- `Dequeue(ctx, timeout time.Duration) (*Claim, error)` — block until a task can be claimed or `timeout` elapses; returns `ErrQueueEmpty` on timeout, `ErrQueueClosed` after `Close`, or `ctx.Err()` on cancellation
- `Stream(ctx, timeout time.Duration, buffer int) (*yielder.Yielder[*Claim], error)` — stream claims into a yielder; the caller controls the stream's lifetime through `ctx`
- `Redrive(ctx, id uint64) error` — move a dead-lettered task back onto the queue; returns `ErrLeaseNotFound` if the DLQ has no such task
- `DeadLetterLen(ctx) (int, error)` — number of dead-lettered tasks
- `Close(ctx) error` — stop the reaper and wait for it to exit; idempotent

**Claim Methods:**

- `Task() Task` — the claimed task
- `Ack(ctx) error` — mark the task done and remove it; a second settle returns `ErrClaimSettled`
- `Nack(ctx, reason error) error` — report failure; the queue requeues, or dead-letters once `WithMaxAttempts` is reached

**Interfaces:**

```go
type Task interface { // already defined alongside Lobby
    Do(ctx context.Context) error
    ID() uint64
    Seq() uint64
    Priority() Priority
}

type Backend interface {
    Enqueue(ctx context.Context, task Task) error
    Claim(ctx context.Context, visibility time.Duration) (Lease, error)
    Ack(ctx context.Context, token string) error
    Nack(ctx context.Context, token string, requeue bool) error
    ReapExpired(ctx context.Context, now time.Time) ([]Lease, error)
    Len(ctx context.Context) (int, error)
}

type DeadLetter interface {
    Add(ctx context.Context, task Task, reason error) error
    List(ctx context.Context, limit int) ([]DeadTask, error)
    Remove(ctx context.Context, id uint64) error
    Len(ctx context.Context) (int, error)
}

type Lease interface {
    Token() string
    Task() Task
    Attempt() int
    Deadline() time.Time
}
```

`NewMemoryBackend(mode Mode)` and `NewMemoryDeadLetter()` are the in-memory defaults.

**Errors:**

- `ErrQueueEmpty` — nothing claimable before `Dequeue`'s timeout
- `ErrQueueClosed` — producer/consumer call after `Close`
- `ErrLeaseNotFound` — ack/nack with an unknown token, or `Redrive` of an absent ID
- `ErrClaimSettled` — second `Ack`/`Nack` on the same claim
- `ErrInvalidQueue` — bad construction (e.g. nil context), joined with detail
- `ErrLeaseExpired` — dead-letter reason used when a lease expiry exhausts attempts
- `ErrTaskNil` — nil task passed to `Enqueue`

## Architecture

### Synchronization Patterns

The library uses several synchronization primitives:

- **`sync/atomic`**: For thread-safe state management and counters
- **`sync.Mutex`**: To protect shared data structures (task lists, slices)
- **`sync.WaitGroup`**: For coordinating goroutine completion and cleanup
- **claims-based Semaphore**: Lock-free concurrency control

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
