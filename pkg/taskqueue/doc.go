// Package taskqueue provides a backend-agnostic queue of tasks that orders
// work by priority or strict FIFO and delivers it with at-least-once
// semantics through a claim/ack/nack lease model.
//
// # Layers
//
// A Lobby (see lobby.go) is the intake/backpressure front door. The Queue
// sits behind it as the ordered storage layer. A Queue is a thin orchestrator
// over two interface seams:
//
//   - Backend owns storage, ordering, and lease mechanics. The default is an
//     in-memory heap (NewMemoryBackend); a PostgreSQL backend would implement
//     the same contract with SELECT ... FOR UPDATE SKIP LOCKED.
//   - DeadLetter stores tasks that exhausted their delivery attempts. The
//     default is in-memory (NewMemoryDeadLetter).
//
// # Ordering
//
// Mode selects the discipline: ModePriority orders by Priority descending,
// FIFO within equal priority; ModeFIFO orders strictly by Seq.
//
// # Delivery
//
// Dequeue claims the next task under a visibility lease and returns a Claim.
// The consumer settles it exactly once: Ack on success, Nack on failure. The
// Queue counts delivery attempts; once a task reaches the configured
// WithMaxAttempts ceiling without an Ack — whether by Nack or by a
// reaper-detected lease expiry — it is moved to the DeadLetter. Redrive
// returns a dead-lettered task to the queue. Requeue is immediate; delayed
// backoff is a planned extension.
//
// # Lifecycle
//
// New starts a background reaper that sweeps expired leases every
// WithReapInterval. Close cancels the reaper and waits for it to exit; it is
// idempotent. Producer and consumer calls after Close return ErrQueueClosed.
package taskqueue
