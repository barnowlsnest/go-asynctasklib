package taskqueue

// QueueEvents observes queue lifecycle transitions. Hooks fire synchronously
// on the calling goroutine, so keep implementations fast. Embed
// NoopQueueEvents to override only the hooks you need.
type QueueEvents interface {
	OnEnqueue(task Task)
	OnClaim(lease Lease)
	OnAck(task Task)
	OnNack(task Task, reason error)
	OnRetry(task Task, attempt int)
	OnDeadLetter(task Task, reason error)
	OnLeaseExpired(lease Lease)
}

// NoopQueueEvents discards every event. Custom observers embed it and
// override only the methods they care about.
type NoopQueueEvents struct{}

// NewNoopQueueEvents returns a no-op observer.
func NewNoopQueueEvents() *NoopQueueEvents { return &NoopQueueEvents{} }

func (*NoopQueueEvents) OnEnqueue(Task)           {}
func (*NoopQueueEvents) OnClaim(Lease)            {}
func (*NoopQueueEvents) OnAck(Task)               {}
func (*NoopQueueEvents) OnNack(Task, error)       {}
func (*NoopQueueEvents) OnRetry(Task, int)        {}
func (*NoopQueueEvents) OnDeadLetter(Task, error) {}
func (*NoopQueueEvents) OnLeaseExpired(Lease)     {}
