package workerpool

import (
	"errors"
)

var (
	ErrMaxPoolSize      = errors.New("max pool size exceeded")
	ErrWorkerTimeout    = errors.New("worker timeout")
	ErrWorkerPanic      = errors.New("worker panic")
	ErrInvalidWorker    = errors.New("invalid worker")
	ErrNilJob           = errors.New("err nil job")
	ErrDispatcherClosed = errors.New("dispatcher closed")
	ErrNoActiveWorkers  = errors.New("no active workers")
)
