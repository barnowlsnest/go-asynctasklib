package task

import (
	"errors"
)

var (
	ErrCancelledTask      = errors.New("task was canceled")
	ErrTaskTimeout        = errors.New("task run timeout")
	ErrNilCtx             = errors.New("context cannot be nil")
	ErrTaskInProgress     = errors.New("task is already in progress")
	ErrTaskFnNotSet       = errors.New("task function is not set")
	ErrMaxRetriesNotSet   = errors.New("maximum retries not set or less than zero")
	ErrMaxRetriesExceeded = errors.New("maximum retries exceeded")
)
