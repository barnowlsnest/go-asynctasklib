package workerpool

import (
	"sync"

	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

type Submission struct {
	mu     sync.Mutex
	err    error
	doneCh chan struct{}
	task   *task.Task
}

func (s *Submission) Done() <-chan struct{} {
	return s.doneCh
}

func (s *Submission) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *Submission) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Submission) Cancel() {
	switch {
	case s == nil:
		return
	case s.task == nil:
		return
	default:
		s.task.Cancel()
	}
}
