package workflow

import "errors"

var (
	ErrPoolStopped = errors.New("pool is stopped")
	ErrMaxWorkers  = errors.New("max workgroup workers should be greater 0")
)
