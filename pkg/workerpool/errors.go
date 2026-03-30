package workerpool

import (
	"errors"
)

var (
	ErrMaxPoolSize          = errors.New("max pool size exceeded")
	ErrWorkerTimeout        = errors.New("worker timeout")
	ErrWorkerPanic          = errors.New("worker panic")
	ErrInvalidWorker        = errors.New("invalid worker")
	ErrNilJob               = errors.New("nil job")
	ErrDispatcherClosed     = errors.New("dispatcher closed")
	ErrNoWorkers            = errors.New("no active workers")
	ErrCfg                  = errors.New("worker pool config")
	ErrInvalidShutdown      = errors.New("invalid shutdown of subscribed worker")
	ErrWorkerAlreadyRunning = errors.New("worker already subscribed")
	ErrNilCtx               = errors.New("nil context")
)
