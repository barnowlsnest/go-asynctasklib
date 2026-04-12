package workerpool

import (
	"fmt"
	"time"
)

// NoopEvents is a zero-value implementation of Events that discards
// every lifecycle and job outcome. Custom observers can embed it and
// override only the methods they need, rather than implementing the
// full interface.
type NoopEvents[T any] struct{}

// NewNoopEvents returns a new no-op Events observer. It is equivalent
// to &NoopEvents[T]{} and exists to match the constructor style of the
// rest of the package.
func NewNoopEvents[T any]() *NoopEvents[T] {
	return &NoopEvents[T]{}
}

func (*NoopEvents[T]) JobOk(_ T) {}

func (*NoopEvents[T]) JobFailed(_ error, _ T) {}

func (*NoopEvents[T]) Subscribed(_ uint64) {}

func (*NoopEvents[T]) Unsubscribed(_ uint64) {}

func (*NoopEvents[T]) SubscribeFailed(_ error, _ uint64) {}

func (*NoopEvents[T]) WorkerStarted(_ uint64) {}

func (*NoopEvents[T]) WorkerStopped(_ uint64) {}

func (*NoopEvents[T]) UnsubscribeFailed(_ error, _ uint64) {}

func (*NoopEvents[T]) LeaveTimeout(_ uint64, _ time.Duration) {}

func (*NoopEvents[T]) DispatchError(err error, j T) {
	fmt.Println(err, j)
}

// NoopHandler is a HandlerFunc that accepts any job and returns nil.
// It is useful for tests and for benchmarking the pool's dispatch path
// independently of handler cost.
func NoopHandler[T any](_ JobAware[T]) error { return nil }
