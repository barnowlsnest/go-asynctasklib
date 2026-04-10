// Package workerpool provides a generic, context-aware worker pool that
// dispatches jobs of type T through a shared Claims dispatcher.
//
// # Modes
//
// The pool supports two modes selected by [Config.Mode]:
//
//   - [ModeFixedSize] creates Size workers and Joins them all on startup.
//     The set of subscribed workers is constant for the pool's lifetime.
//
//   - [ModeAutoScale] starts with MinSize workers Joined and scales up (to
//     MaxSize) when the backlog grows. Workers idle beyond IdleTimeout are
//     Left and re-Joined the next time the pool needs them.
//
// # Dispatch model
//
// Instead of fanning a job out across one queue per worker, every idle
// worker publishes a [Claim] (its own input channel) onto a shared,
// buffered channel. [WorkerPool.Submit] pops the next Claim and hands the
// job directly to that worker. This gives O(1) dispatch with no per-submit
// scan over the worker set and no global lock on the happy path.
//
// # Lifecycle and safety
//
// Handler panics are recovered inside the worker and surfaced as
// [ErrWorkerPanic] via [Events.JobFailed]; the worker keeps serving after
// a panic. [WorkerPool.Close] is idempotent, cancels in-flight work via
// the pool's context, and rejects post-close submissions with
// [ErrPoolShutdown].
//
// # Observability
//
// Callers can pass any implementation of the [Events] interface to
// observe worker lifecycle transitions and per-job outcomes. [NoopEvents]
// is provided as a zero-value base that custom observers can embed and
// override selectively.
//
// # Basic usage
//
//	handler := func(ctx context.Context, job *int) error {
//	    return process(*job)
//	}
//
//	pool, err := workerpool.New(ctx,
//	    workerpool.WithConfig[int](&workerpool.Config{
//	        Mode: workerpool.ModeFixedSize,
//	        ClaimsConfig: workerpool.ClaimsConfig{
//	            Size:          8,
//	            SubmitTimeout: 2 * time.Second,
//	        },
//	        Backlog:   100,
//	        RateLimit: 1000,
//	    }),
//	    workerpool.WithHandler(handler),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer pool.Close()
//
//	job := 42
//	if err := pool.Submit(&job); err != nil {
//	    log.Printf("submit: %v", err)
//	}
package workerpool
