package yielder

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultTimeout = time.Second
	defaultBuf     = 1
)

// Yielder is a one-shot, generic value generator that emits results through a channel.
// It runs a generator function in a background goroutine and sends produced values
// to a buffered channel. A separate timeout watchdog goroutine auto-stops the yielder
// if generation takes too long. Panics in the generator are recovered and surfaced as errors.
type Yielder[T comparable] struct {
	genChan  chan *T
	doneCh   chan struct{}
	fn       func() ([]*T, error)
	err      error
	timeout  time.Duration
	buf      int
	mu       sync.Mutex
	onceStop sync.Once
}

// Option is a functional option for configuring a Yielder.
type Option[T comparable] func(*Yielder[T])

// WithTimeout sets the maximum time the yielder may run before being auto-stopped.
// Defaults to 1 second if not specified.
func WithTimeout[T comparable](t time.Duration) Option[T] {
	return func(y *Yielder[T]) {
		y.timeout = t
	}
}

// WithBuffer sets the result channel buffer size. Defaults to 1 if not specified or <= 0.
func WithBuffer[T comparable](buf int) Option[T] {
	return func(y *Yielder[T]) {
		y.buf = buf
	}
}

// WithGeneratorFunc sets the generator function that produces values.
// The function is called once; returned values are emitted through the Results channel.
func WithGeneratorFunc[T comparable](fn func() ([]*T, error)) Option[T] {
	return func(y *Yielder[T]) {
		y.fn = fn
	}
}

// WithValues wraps a static slice into a generator function.
func WithValues[T comparable](values []*T) Option[T] {
	return func(y *Yielder[T]) {
		y.fn = func() ([]*T, error) {
			return values, nil
		}
	}
}

// New creates and starts a Yielder. It returns an error if the context is already
// done or if no generator function is provided. The yielder begins generating
// values immediately in a background goroutine.
func New[T comparable](ctx context.Context, opts ...Option[T]) (*Yielder[T], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var y Yielder[T]
	for _, opt := range opts {
		opt(&y)
	}

	if y.fn == nil {
		return nil, fmt.Errorf("invalid generator func: %w", ErrNil)
	}
	if y.timeout == time.Duration(0) {
		y.timeout = defaultTimeout
	}
	if y.buf <= 0 {
		y.buf = defaultBuf
	}

	y.genChan = make(chan *T, y.buf)
	y.doneCh = make(chan struct{})
	go generate[T](ctx, &y)
	go checkTimeout[T](ctx, &y)

	return &y, nil
}

// Results returns a read-only channel of generated values. The channel is closed
// when generation completes, is stopped, or the context is canceled.
func (yr *Yielder[T]) Results() <-chan *T {
	return yr.genChan
}

// Err returns accumulated errors from generation. Multiple errors are joined via errors.Join.
func (yr *Yielder[T]) Err() error {
	yr.mu.Lock()
	defer yr.mu.Unlock()
	return yr.err
}

// Done returns a channel that is closed when the yielder finishes, regardless of
// whether it completed successfully, errored, timed out, or was stopped.
func (yr *Yielder[T]) Done() <-chan struct{} {
	return yr.doneCh
}

func (yr *Yielder[T]) setErr(err error) {
	yr.mu.Lock()
	defer yr.mu.Unlock()
	if yr.err != nil {
		yr.err = errors.Join(yr.err, err)
		return
	}

	yr.err = err
}

// Stop stops the yielder. It is safe to call multiple times.
func (yr *Yielder[T]) Stop() {
	yr.onceStop.Do(func() {
		close(yr.doneCh)
	})
}

func generate[T comparable](ctx context.Context, y *Yielder[T]) {
	defer close(y.genChan)
	func(y *Yielder[T]) {
		defer func() {
			if r := recover(); r != nil {
				y.setErr(fmt.Errorf("err panic'ed generator func: %+v", r))
			}

			y.onceStop.Do(func() {
				close(y.doneCh)
			})
		}()

		values, errGen := y.fn()
		if errGen != nil {
			y.setErr(fmt.Errorf("err func generator: %w", errGen))
			return
		}

		for _, val := range values {
			select {
			case <-ctx.Done():
				y.setErr(ctx.Err())
				return
			case <-y.doneCh:
				y.setErr(ErrStopped)
				return
			case y.genChan <- val:
			}
		}
	}(y)
}

func checkTimeout[T comparable](ctx context.Context, y *Yielder[T]) {
	select {
	case <-ctx.Done():
		return
	case <-y.doneCh:
		return
	case <-time.After(y.timeout):
		y.Stop()
		return
	}
}
