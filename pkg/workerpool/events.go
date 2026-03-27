package workerpool

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
