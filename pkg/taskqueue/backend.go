package taskqueue

import (
	"context"
	"time"
)

// Lease is a claimed task held under a visibility deadline. Token is opaque
// and identifies the claim for Ack/Nack. Attempt is the delivery count
// (1 on first claim).
type Lease interface {
	Token() string
	Task() Task
	Attempt() int
	Deadline() time.Time
}

// Backend owns storage, ordering, and lease mechanics. Implementations must
// be safe for concurrent use. The in-memory backend is the default; a
// PostgreSQL backend would implement the same contract with SELECT ... FOR
// UPDATE SKIP LOCKED.
type Backend interface {
	// Enqueue stores task as ready to claim.
	Enqueue(ctx context.Context, task Task) error
	// Claim atomically selects the next eligible task per the backend's Mode,
	// increments its attempt counter, marks it leased until now+visibility,
	// and returns a Lease. Returns ErrQueueEmpty when nothing is claimable.
	Claim(ctx context.Context, visibility time.Duration) (Lease, error)
	// Ack permanently removes the leased task.
	Ack(ctx context.Context, token string) error
	// Nack releases the lease. When requeue is true the task becomes
	// claimable again with its attempt count preserved; otherwise it is
	// dropped from the backend.
	Nack(ctx context.Context, token string, requeue bool) error
	// ReapExpired returns the leases whose deadline is at or before now,
	// without changing their state, so the Queue can apply retry/DLQ policy.
	ReapExpired(ctx context.Context, now time.Time) ([]Lease, error)
	// Len reports the number of tasks waiting to be claimed.
	Len(ctx context.Context) (int, error)
}
