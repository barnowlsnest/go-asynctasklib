package taskgroup

import "errors"

var (
	ErrTaskGroupStopped    = errors.New("task group is stopped")
	ErrTaskGroupMaxWorkers = errors.New("max workers must be greater than 0")
)
