package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	
	"github.com/google/uuid"
)

const (
	CREATED = iota
	PENDING
	STARTED
	DONE
	FAILED
	CANCELED
	
	defaultTimeout = time.Second * 30
)

type (
	Run struct {
		ID      func() uuid.UUID
		Context func() context.Context
		Cancel  context.CancelFunc
	}
	
	Definition struct {
		Delay       time.Duration
		MaxDuration time.Duration
		MaxRetries  int
		TaskFn      func(*Run) error
	}
	
	Task struct {
		timeout    time.Duration
		delay      time.Duration
		id         uuid.UUID
		err        error
		maxRetries int
		state      atomic.Uint32
		wait       sync.WaitGroup
		mu         sync.RWMutex
		fn         func(*Run) error
		cancel     context.CancelFunc
	}
)

func New(d Definition) *Task {
	t := &Task{
		id:         uuid.New(),
		cancel:     func() {},
		fn:         func(*Run) error { return ErrTaskFnNotSet },
		timeout:    defaultTimeout,
		delay:      d.Delay,
		maxRetries: d.MaxRetries,
	}
	t.state.Store(CREATED)
	if d.TaskFn != nil {
		t.fn = d.TaskFn
	}
	if d.MaxDuration > 0 {
		t.timeout = d.MaxDuration
	}
	return t
}

func (t *Task) run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		t.state.Store(CANCELED)
		return errors.Join(ErrCancelledTask, ctx.Err())
	case <-time.After(t.timeout):
		t.state.Store(FAILED)
		return ErrTaskTimeout
	case err := <-t.delegateFn(ctx):
		if err != nil {
			t.state.Store(FAILED)
			return err
		}
		t.state.Store(DONE)
		return nil
	}
}

func (t *Task) delegateFn(ctx context.Context) <-chan error {
	errCh := make(chan error)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("task panicked: %v", r)
			}
			close(errCh)
		}()
		select {
		case <-ctx.Done():
			errCh <- ctx.Err()
			return
		default:
			run := &Run{
				Cancel:  t.cancel,
				ID:      func() uuid.UUID { return t.id },
				Context: func() context.Context { return ctx },
			}
			if err := t.fn(run); err != nil {
				errCh <- err
				return
			}
			errCh <- nil
			return
		}
	}()
	return errCh
}

func (t *Task) Go(ctx context.Context) error {
	if ctx == nil {
		return ErrNilCtx
	}
	if t.IsInProgress() {
		return ErrTaskInProgress
	}
	t.state.Store(PENDING)
	select {
	case <-ctx.Done():
		t.state.Store(CANCELED)
		return errors.Join(ErrCancelledTask, ctx.Err())
	case <-time.After(t.delay):
		compCtx, cancel := context.WithCancel(ctx)
		t.cancel = cancel
		t.wait.Add(1)
		go func() {
			defer t.wait.Done()
			t.mu.Lock()
			if err := t.run(compCtx); err != nil {
				t.err = err
			}
			t.mu.Unlock()
		}()
		t.state.Store(STARTED)
		return nil
	}
}

func (t *Task) GoRetry(ctx context.Context) error {
	if t.maxRetries <= 0 {
		t.state.Store(FAILED)
		return ErrMaxRetriesNotSet
	}
	
	var a int
attempt:
	if a >= t.maxRetries {
		return ErrMaxRetriesExceeded
	}
	if err := t.Go(ctx); err != nil {
		return err
	}
	t.Wait()
	if t.IsFailed() {
		a++
		goto attempt
	}
	return nil
}

func (t *Task) Cancel() {
	defer t.state.Store(CANCELED)
	t.cancel()
}

func (t *Task) IsCreated() bool {
	return t.state.Load() == CREATED
}

func (t *Task) IsPending() bool {
	return t.state.Load() == PENDING
}

func (t *Task) IsStarted() bool {
	return t.state.Load() == STARTED
}

func (t *Task) IsDone() bool {
	return t.state.Load() == DONE
}

func (t *Task) IsFailed() bool {
	return t.state.Load() == FAILED
}

func (t *Task) IsCanceled() bool {
	return t.state.Load() == CANCELED
}

func (t *Task) IsEnd() bool {
	s := t.state.Load()
	return s == DONE || s == FAILED || s == CANCELED
}

func (t *Task) IsInProgress() bool {
	s := t.state.Load()
	return s == STARTED || s == PENDING
}

func (t *Task) Err() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.err
}

func (t *Task) Wait() {
	t.wait.Wait()
}

func (t *Task) ID() uuid.UUID {
	return t.id
}
