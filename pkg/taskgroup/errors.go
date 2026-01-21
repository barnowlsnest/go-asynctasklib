package taskgroup

import "errors"

var (
	ErrTaskGroupStopped    = errors.New("pool is stopped")
	ErrTaskGroupMaxWorkers = errors.New("max workgroup workers should be greater 0")
)
