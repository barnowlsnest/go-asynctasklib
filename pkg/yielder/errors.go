package yielder

import (
	"errors"
)

var (
	ErrNil     = errors.New("err nil")
	ErrTimeout = errors.New("timeout")
	ErrStopped = errors.New("stopped")
)
