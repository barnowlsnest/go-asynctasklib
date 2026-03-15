# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## CRITICAL REQUIREMENT: ALWAYS RUN `task sanity`

**YOU MUST ALWAYS run `task sanity` after ANY code change in this repository. This is non-negotiable.**

```bash
task sanity
```

This command runs `go mod tidy`, formatting, vetting, linting, and tests. A code change is NOT complete until `task sanity` passes successfully. Do not skip this step under any circumstances.

## Project Overview

`go-asynctasklib` is a Go library for managing asynchronous tasks with lifecycle hooks, retry logic, timeouts, and state management. The library includes:
- **Task package** (`pkg/task`) - Core async task execution with state management, hooks, retries, and timeouts
- **Retry package** (`pkg/retry`) - Pluggable retry strategies (linear, exponential backoff) with jitter; constant delay is built into task Definition
- **TaskGroup package** (`pkg/taskgroup`) - Manages concurrent task execution with configurable worker limits
- **Yielder package** (`pkg/yielder`) - Generic one-shot value generator with channel output, timeout, and panic recovery
- **Semaphore package** (`pkg/semaphore`) - Channel-based semaphore for custom concurrency control

The library uses atomic operations and goroutines for safe concurrent task execution.

## Requirements

- **Go 1.25** (see `go.mod`)
- [Task](https://taskfile.dev) runner (`task` command)
- [golangci-lint](https://golangci-lint.run/)

## Development Commands

This project uses [Task](https://taskfile.dev) for build automation. All commands should be run via `task`:

### Common Commands
```bash
task go-update                   # Run go mod tidy
task go-test                     # Run all tests with coverage
task go-fmt                      # Format code
task go-vet                      # Run go vet
task go-lint                     # Run golangci-lint with auto-fix
task sanity                      # Run go-update, fmt, vet, lint, and test (in order)
task build                       # Run sanity checks and build
```

### Direct Go Commands (for specific needs)
```bash
go test ./pkg/task -v -race       # Run task tests with race detector
go test ./pkg/retry -v -race      # Run retry tests with race detector
go test ./pkg/taskgroup -v -race  # Run taskgroup tests with race detector
go test ./pkg/semaphore -v -race  # Run semaphore tests with race detector
go test ./pkg/yielder -v -race    # Run yielder tests with race detector
go test ./... -race -cover        # Run all tests with race detector and coverage
```

## Architecture

### Core Components

#### Task Package (`pkg/task`)

**Task Lifecycle States** (`pkg/task/state.go:8-14`):
- `CREATED` → `PENDING` → `STARTED` → `DONE|FAILED|CANCELED`
- State transitions are atomic and managed via `atomic.Uint32`

**RunFunc Type** (`pkg/task/task.go:17`):
- `type RunFunc func(*Run) error` - Type alias for task functions

**Definition Struct** (`pkg/task/task.go:27-36`):
- `ID uint64` - Unique task identifier
- `Name string` - Human-readable task name
- `TaskFn RunFunc` - Task function to execute
- `Hooks *StateHooks` - State change callbacks
- `Delay time.Duration` - Delay before starting
- `MaxDuration time.Duration` - Execution timeout (default: 30s)
- `RetryDelay time.Duration` - Constant delay between retries (used when RetryStrategy is nil)
- `MaxRetries int` - Number of retry attempts (0 = no retries)
- `RetryStrategy retry.Strategy` - Pluggable retry delay strategy (nil = use RetryDelay)

**Builder Pattern** (`pkg/task/builder.go`):
- `NewBuilder(opts ...OptionFunc) *Builder` - Create a new builder with options
- `Build() (*Definition, error)` - Build and validate the definition
- Option functions: `WithID`, `WithName`, `WithTaskFn`, `WithMaxRetries`, `WithDelay`, `WithTimeout`, `WithHooks`, `WithRetryDelay`, `WithRetryStrategy`
- Validation errors: `ErrIDNotSet`, `ErrTaskFnNotSet`, `ErrMaxRetriesNotSet`
- Auto-generates `Name` as "task-{ID}" if not provided

**Task Struct** (`pkg/task/task.go:38-53`):
- Uses `sync.WaitGroup` for goroutine coordination
- `atomic.Uint32` for thread-safe state management
- Context-based cancellation with `context.CancelFunc`
- Mutex protects `err` and `cancel` fields
- Fields ordered for optimal memory alignment
- Methods: `ID() uint64`, `Name() string`, `Go()`, `GoRetry()`, `Await()`, `Cancel()`, etc.

**State Hooks System** (`pkg/task/state.go:16-120`):
- Observer pattern for lifecycle events
- Hooks are panic-safe (panics are silently caught via `catchPanic()`)
- All hooks receive task ID (`uint64`) and timestamp
- Configured via functional options pattern

#### Retry Package (`pkg/retry`)

**Strategy Interface** (`pkg/retry/strategy.go`):
- `Strategy` interface with single method: `Delay(attempt int) time.Duration`
- Configurable via functional options: `WithBaseDelay(d)`, `WithMaxDelay(d)`, `WithJitter(bool)`
- Defaults: baseDelay=100ms, maxDelay=30s, jitter=false
- Helpers: `capDelay` (caps at maxDelay), `applyJitter` (randomizes in [0, delay] using `crypto/rand`)

**Strategies** (for variable delay patterns):
- `NewLinear(opts ...OptionFunc) *Linear` - Returns `baseDelay * (attempt + 1)`, capped at `maxDelay`
- `NewExponentialBackoff(opts ...OptionFunc) *ExponentialBackoff` - Returns `baseDelay * 2^attempt`, capped at `maxDelay`, with overflow protection

**Constant delay** is handled natively via `Definition.RetryDelay` — no strategy object needed.

**Integration with Task:**
- `Definition.RetryDelay time.Duration` - Constant delay between retries (used when RetryStrategy is nil)
- `Definition.RetryStrategy retry.Strategy` - Set on task definition for variable delay patterns (overrides RetryDelay)
- `GoRetry()` applies strategy delay (or RetryDelay fallback) between retry attempts, respecting context cancellation
- Builder options: `WithRetryDelay(delay)`, `WithRetryStrategy(strategy)`

#### TaskGroup Package (`pkg/taskgroup`)

**TaskGroup** (`pkg/taskgroup/taskgroup.go`):
- Manages concurrent task execution with configurable worker limits
- Uses `semaphore.Semaphore` for worker concurrency control
- Integrates with `golang.org/x/sync/errgroup` for coordinated error handling
- Thread-safe with `sync.Mutex` protecting shared state and `sync.WaitGroup` tracking semaphore releases
- TaskGroup struct fields:
  - `sem *semaphore.Semaphore` - Controls max concurrent workers
  - `mu sync.Mutex` - Protects `tasks` slice and `releaseWg`
  - `tasks []*task.Task` - All submitted tasks
  - `releaseWg sync.WaitGroup` - Tracks semaphore release goroutines
  - `stopped atomic.Bool` - TaskGroup stopped state
- Constructor: `taskgroup.New(maxWorkers int) (*TaskGroup, error)` - Returns error if maxWorkers <= 0
- Key methods:
  - `Submit(ctx, Definition) (*Task, error)` - Submit task and return immediately
  - `SubmitBatch(ctx, []Definition) ([]*Task, error)` - Submit multiple tasks
  - `SubmitWithErrGroup(ctx, []Definition) error` - Submit with errgroup coordination
  - `Wait()` - Wait for all tasks AND semaphore releases to complete
  - `Stop()` / `StopWithContext(ctx) error` - Graceful/forceful shutdown
  - `Tasks() []*Task` - Get copy of all submitted tasks
  - `IsStopped() bool` - Check if TaskGroup is stopped
  - `Stats() PoolStats` - Get statistics about tasks

#### Yielder Package (`pkg/yielder`)

**Yielder** (`pkg/yielder/yielder.go`):
- Generic, one-shot value generator that emits results through a channel
- Runs generator function in a goroutine, sends values via buffered channel
- Separate timeout watchdog goroutine auto-stops idle yielders
- Thread-safe error accumulation via `sync.Mutex` and `errors.Join`
- `sync.Once` ensures `doneCh` is closed exactly once across `Stop()` and `generate` defer
- Yielder struct fields:
  - `timeout time.Duration` - Timeout before auto-stop (default: 1s)
  - `buf int` - Result channel buffer size (default: 1)
  - `mu sync.Mutex` - Protects `err` field
  - `onceStop sync.Once` - Ensures single close of `doneCh`
  - `genChan chan *T` - Buffered channel for emitting values
  - `doneCh chan struct{}` - Closed when yielder finishes
  - `fn func() ([]*T, error)` - Generator function
  - `err error` - Accumulated errors
- Constructor: `New[T](ctx, opts ...Option[T]) (*Yielder[T], error)` - Returns error if ctx is done or fn is nil
- Options: `WithTimeout`, `WithBuffer`, `WithGeneratorFunc`, `WithValues`
- Key methods:
  - `Results() <-chan *T` - Read-only channel of generated values
  - `Done() <-chan struct{}` - Closed on completion
  - `Stop()` - Idempotent stop via `sync.Once`
  - `Err() error` - Mutex-protected error accessor
- Internal:
  - `generate()` - Goroutine that calls `fn`, sends values with `select` on `ctx.Done`/`doneCh`, recovers panics
  - `checkTimeout()` - Goroutine that calls `Stop()` after timeout, exits early on `ctx.Done` or `doneCh`
- Errors: `ErrNil` (nil generator), `ErrTimeout` (unused directly, timeout triggers `ErrStopped`), `ErrStopped` (stopped during emission)

#### Semaphore Package (`pkg/semaphore`)

**Semaphore** (`pkg/semaphore/semaphore.go`):
- Channel-based semaphore for concurrency control
- Supports blocking (`Acquire`), non-blocking (`TryAcquire`), context-aware (`AcquireWithContext`), and timeout-based (`TryAcquireWithTimeout`) acquisition
- Query methods: `Limit()`, `Available()`, `Acquired()`

### Key Design Patterns

1. **Functional Options**: Used for `StateHooks` configuration via `StateHookOpt` and `retry.OptionFunc` for strategy config
2. **Strategy Pattern**: Pluggable `retry.Strategy` interface for configurable retry delay behavior
3. **Builder Pattern**: `Builder` with `OptionFunc` for constructing validated `Definition` structs
4. **Atomic State Machine**: All state transitions use `atomic.Store/Load` for thread safety
5. **Goroutine Delegation**: Task execution happens in separate goroutine with error channel communication
6. **Context Propagation**: All operations receive context for cancellation and timeout control
7. **Channel-based Semaphore**: Uses buffered channel for lock-free concurrency control
8. **WaitGroup Coordination**: `releaseWg` in TaskGroup ensures `Wait()` waits for ALL cleanup, not just task completion

### Important Implementation Details

**Task Package:**
- **Retry Logic** (`pkg/task/task.go:216-249`): Uses `goto` for retry loop with attempt counting and optional strategy-based delay
- **Retry Strategy Delay** (`pkg/task/task.go:234-244`): Context-aware `select` between `time.After(delay)` and `ctx.Done()` during retry backoff
- **Timeout Handling** (`pkg/task/task.go:90-117`): Select statement with timeout channel and context cancellation
- **Error Handling**: All errors are stored in mutex-protected `err` field and joined with `errors.Join`
- **Context Lifecycle**: Created in `Go()` after delay, stored in mutex-protected field for later cancellation

**TaskGroup Package:**
- **Semaphore Release Tracking**: TaskGroup uses `releaseWg.Go()` in `Submit()` to track semaphore releases
- **Wait() Guarantees**: `Wait()` waits for both task completion (`t.Await()`) AND semaphore releases (`releaseWg.Wait()`)
- **Mutex Usage**: `tg.mu` protects `tasks` slice operations, NOT the semaphore (which has its own concurrency control)
- **Memory Alignment**: Struct fields are ordered for optimal memory layout (pointers first, then values)

### Error Types

- **Task errors** (`pkg/task/errors.go`): `ErrTaskXxx` pattern
- **TaskGroup errors** (`pkg/taskgroup/errors.go`): `ErrTaskGroupStopped`, `ErrTaskGroupMaxWorkers`
- **Yielder errors** (`pkg/yielder/errors.go`): `ErrNil`, `ErrTimeout`, `ErrStopped`

## Linting Constraints (`.golangci.yaml`)

Code must pass `golangci-lint` with these key limits:
- **Max function length**: 100 lines / 50 statements (`funlen`)
- **Max cyclomatic complexity**: 15 (`gocyclo`)
- **Max line length**: 140 characters (`lll`)
- **Duplication threshold**: 100 tokens (`dupl`)
- **goimports** with local prefix `github.com/barnowlsnest/go-asynctasklib`
- **Test files** (`_test.go`) are excluded from: `dupl`, `funlen`, `goconst`, `gocyclo`, `gosec`

## Code Style & Best Practices

- Use `sync/atomic` for state management (especially `atomic.Uint32`, `atomic.Bool`)
- All public methods should be context-aware where appropriate
- Hook functions must be wrapped with panic recovery (`defer catchPanic()`)
- Struct field ordering prioritizes memory alignment (pointers → large structs → primitives)
- Use `sync.Mutex` to protect shared data structures (slices, maps, non-atomic fields)
- Semaphores control concurrency, mutexes protect data - don't confuse the two
- Always use `defer` with mutexes, but be careful not to double-unlock
- When using WaitGroups, protect `Add()` operations with the same mutex that protects `Wait()`
- Test with `-race` flag to catch concurrency issues
- Use testify `suite.Suite` for organizing tests — one suite per logical component, test methods named `Test<Behavior>`
- Use suite assertions (`s.Equal`, `s.NoError`, etc.) instead of standalone `assert`/`require` within suites
- Benchmarks remain standalone `Benchmark*` functions (suites don't support benchmarks)

## Final Reminder

**ALWAYS run `task sanity` after ANY code change. No exceptions. This is the final step for every modification.**