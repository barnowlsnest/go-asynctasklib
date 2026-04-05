package workerpool

import (
	"errors"
	"fmt"
)

var (
	ErrNil    = errors.New("nil error")
	ErrNilJob = fmt.Errorf("%w: nil job", ErrNil)
	ErrNilCtx = fmt.Errorf("%w: nil context", ErrNil)
)
