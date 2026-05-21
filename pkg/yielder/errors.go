package yielder

import (
	"errors"
)

var (
	ErrNil         = errors.New("err nil")
	ErrStopped     = errors.New("stopped")
	ErrInputClosed = errors.New("input closed")
)
