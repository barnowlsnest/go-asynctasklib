// Package workerpool provides a generic, fixed-size pool of goroutines
// that dispatch submitted jobs to the first idle worker via a claims-based
// rendezvous channel. It is intended as a building block for services that
// need bounded concurrency, backpressure, and predictable shutdown.
//
// # Lifecycle
//
// Construct a pool with [New], providing a context, a [Config], a
// [HandlerFunc], and (optionally) a [PoolEvents] observer. New starts the
// dispatcher goroutine and subscribes all workers. Call [WorkerPool.Submit]
// to enqueue jobs. Call [WorkerPool.GracefulShutdown] to drain the backlog
// and let in-flight handlers finish, or [WorkerPool.Shutdown] to cancel the
// pool context immediately and let handlers observe cancellation through
// their [JobAware] argument.
//
// # Configuration
//
// [Config] controls the three knobs that matter operationally:
//
//   - ClaimsConfig.Size — number of worker goroutines (fixed in
//     ModeFixedSize). Defaults to runtime.NumCPU.
//   - Backlog — buffered depth of the internal job channel. Submit blocks
//     on a full backlog up to ClaimsConfig.SubmitTimeout.
//   - RateLimit — jobs per second accepted into the backlog. Enforced by a
//     token-bucket limiter with burst equal to ClaimsConfig.Size.
//
// # Events
//
// [PoolEvents] is the observer interface for worker and job lifecycle
// transitions. Embed [NoopEvents] and override only the hooks you care
// about. Hooks fire synchronously on the worker goroutine, so keep
// implementations fast; offload I/O to a separate goroutine.
//
// # Shutdown semantics
//
// GracefulShutdown blocks until the dispatcher drains every job already in
// the backlog. Jobs that were in-flight on a worker run to completion. Jobs
// submitted after GracefulShutdown returns ErrPoolShutdown.
//
// Shutdown cancels the pool context immediately. In-flight handlers observe
// cancellation via their JobAware context; queued jobs never dispatch. Both
// shutdown paths are idempotent and safe to call concurrently.
package workerpool
