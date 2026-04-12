package workerpool

import (
	"errors"
	"fmt"
)

var (
	// ErrNil is the base error wrapped by every "nil X" condition the
	// package returns via errors.Join or fmt.Errorf("%w: ...").
	ErrNil = errors.New("nil error")
	// ErrNilJob is returned when a nil job pointer is passed to Submit
	// or into the claims dispatcher.
	ErrNilJob = fmt.Errorf("%w: nil job", ErrNil)
	// ErrNilCtx is returned when a required context.Context is nil.
	ErrNilCtx = fmt.Errorf("%w: nil context", ErrNil)
	// ErrPoolShutdown is returned from WorkerPool.Submit after Close has
	// flipped the pool into rejecting mode.
	ErrPoolShutdown = errors.New("worker pool is shut down")
)
