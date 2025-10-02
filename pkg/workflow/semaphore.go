package workflow

import (
	"context"
	"time"
)

type (
	Semaphore struct {
		sem chan struct{}
	}
)

func NewSemaphore(limit int) *Semaphore {
	return &Semaphore{
		sem: make(chan struct{}, limit),
	}
}

func (s *Semaphore) Acquire() {
	s.sem <- struct{}{}
}

func (s *Semaphore) TryAcquire() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Semaphore) AcquireWithContext(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Semaphore) TryAcquireWithTimeout(timeout time.Duration) bool {
	select {
	case s.sem <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *Semaphore) Release() {
	<-s.sem
}

func (s *Semaphore) Limit() int {
	return cap(s.sem)
}

func (s *Semaphore) Available() int {
	return cap(s.sem) - len(s.sem)
}

func (s *Semaphore) Acquired() int {
	return len(s.sem)
}
