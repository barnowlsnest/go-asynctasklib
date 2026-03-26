package workerpool

import (
	"errors"
)

var (
	ErrMaxPoolSize      = errors.New("err max pool size exceeded")
	ErrWorkerTimeout    = errors.New("err worker timeout")
	ErrSubmitTimeout    = errors.New("err submit timeout")
	ErrWorkerPanic      = errors.New("worker panic")
	ErrInvalidWorker    = errors.New("err invalid worker")
	ErrNilJob           = errors.New("err nil job")
	ErrDispatcherClosed = errors.New("err dispatcher closed")
	ErrNoActiveWorkers  = errors.New("err no active workers")
	ErrNilCfg           = errors.New("err nil worker pool config")
)
