package workerpool

import (
	"context"
	"time"
)

type NoopEvents[T any] struct{}

func NewNoopEvents[T any]() *NoopEvents[T] {
	return &NoopEvents[T]{}
}

func (*NoopEvents[T]) JobOk(_ *T) {}

func (*NoopEvents[T]) JobFailed(_ error, _ *T) {}

func (*NoopEvents[T]) Subscribed(_ uint64) {}

func (*NoopEvents[T]) Unsubscribed(_ uint64) {}

func (*NoopEvents[T]) SubscribeFailed(_ error, _ uint64) {}

func (*NoopEvents[T]) WorkerStarted(_ uint64) {}

func (*NoopEvents[T]) WorkerStopped(_ uint64) {}

func (*NoopEvents[T]) UnsubscribeFailed(_ error, _ uint64) {}

func (*NoopEvents[T]) LeaveTimeout(_ uint64, _ time.Duration) {}

func NoopHandler[T any](_ context.Context, _ *T) error { return nil }
