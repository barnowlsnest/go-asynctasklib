package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const defaultTimeout = time.Second * 30

type (
	Run struct {
		ID       func() string
		Context  func() context.Context
		Cancel   context.CancelFunc
		Callback func()
	}

	Definition struct {
		ID          string
		TaskFn      func(*Run) error
		Hooks       *StateHooks
		Delay       time.Duration
		MaxDuration time.Duration
		MaxRetries  int
	}

	Task struct {
		id         string
		fn         func(*Run) error
		cancel     context.CancelFunc
		hooks      *StateHooks
		wg         sync.WaitGroup
		mu         sync.Mutex
		state      atomic.Uint32
		attempts   atomic.Uint32
		timeout    time.Duration
		delay      time.Duration
		maxRetries int
		err        error
	}
)

func New(d Definition) *Task {
	hooks := NewStateHooks()
	if d.Hooks != nil {
		hooks = d.Hooks
	}

	t := &Task{
		id:         d.ID,
		cancel:     func() {},
		fn:         func(*Run) error { return ErrTaskFnNotSet },
		timeout:    defaultTimeout,
		delay:      d.Delay,
		maxRetries: d.MaxRetries,
		hooks:      hooks,
	}

	if d.TaskFn != nil {
		t.fn = d.TaskFn
	}
	if d.MaxDuration > 0 {
		t.timeout = d.MaxDuration
	}

	t.created()

	return t
}

func (t *Task) run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		t.canceled()
		return errors.Join(ErrCancelledTask, ctx.Err())
	case <-time.After(t.timeout):
		select {
		case <-ctx.Done():
			t.canceled()
			return errors.Join(ErrCancelledTask, ctx.Err())
		default:
			t.failed(ErrTaskTimeout)
			return ErrTaskTimeout
		}
	case err := <-t.delegateFn(ctx):
		switch {
		case errors.Is(err, context.Canceled):
			t.canceled()
			return errors.Join(ErrCancelledTask, err)
		case errors.Is(err, nil):
			t.done()
			return nil
		default:
			t.failed(err)
			return err
		}
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
				Cancel:   t.cancel,
				ID:       func() string { return t.id },
				Context:  func() context.Context { return ctx },
				Callback: func() { t.hooks.onTaskFn(t.id, time.Now().UTC()) },
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

func (t *Task) pending() {
	t.state.Store(PENDING)
	t.hooks.onPending(t.id, time.Now().UTC(), int(t.attempts.Load()))
}

func (t *Task) canceled() {
	t.state.Store(CANCELED)
	t.hooks.onCanceled(t.id, time.Now().UTC())
}

func (t *Task) started() {
	t.state.Store(STARTED)
	t.hooks.onStarted(t.id, time.Now().UTC())
}

func (t *Task) failed(err error) {
	t.state.Store(FAILED)
	t.hooks.onFailed(t.id, time.Now().UTC(), err)
}

func (t *Task) done() {
	t.state.Store(DONE)
	t.hooks.onDone(t.id, time.Now().UTC())
}

func (t *Task) created() {
	t.state.Store(CREATED)
	t.hooks.onCreated(t.id, time.Now().UTC())
}

func (t *Task) Go(ctx context.Context) error {
	if ctx == nil {
		return ErrNilCtx
	}
	if t.IsInProgress() {
		return ErrTaskInProgress
	}

	t.pending()

	select {
	case <-ctx.Done():
		t.canceled()
		return errors.Join(ErrCancelledTask, ctx.Err())
	case <-time.After(t.delay):
		compCtx, cancel := context.WithCancel(ctx)
		t.mu.Lock()
		t.cancel = cancel
		t.mu.Unlock()

		t.wg.Go(func() {
			if err := t.run(compCtx); err != nil {
				t.mu.Lock()
				t.err = err
				t.mu.Unlock()
			}
		})

		t.started()

		return nil
	}
}

func (t *Task) GoRetry(ctx context.Context) error {
	if t.maxRetries <= 0 {
		t.state.Store(FAILED)
		return ErrMaxRetriesNotSet
	}

attempt:
	if int(t.attempts.Load()) >= t.maxRetries {
		return ErrMaxRetriesExceeded
	}
	if err := t.Go(ctx); err != nil {
		return err
	}

	t.Await()

	if t.IsFailed() {
		t.attempts.Add(1)
		goto attempt
	}

	return nil
}

func (t *Task) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancel()
}

func (t *Task) State() uint32 {
	return t.state.Load()
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
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *Task) Await() {
	t.wg.Wait()
}

func (t *Task) ID() string {
	return t.id
}
