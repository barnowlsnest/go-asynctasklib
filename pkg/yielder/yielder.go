package yielder

import (
	"context"
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
	genChan  chan T
	doneCh   chan struct{}
	fn       func(context.Context) ([]T, error)
	cancel   context.CancelFunc
	err      error
	timeout  time.Duration
	buf      int
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
func WithGeneratorFunc[T comparable](fn func(context.Context) ([]T, error)) Option[T] {
	return func(y *Yielder[T]) {
		y.fn = fn
	}
}

// WithValues wraps a static slice into a generator function.
func WithValues[T comparable](values []T) Option[T] {
	return func(y *Yielder[T]) {
		y.fn = func(_ context.Context) ([]T, error) {
			return values, nil
		}
	}
}

func WithInputChannel[T comparable](input <-chan T) Option[T] {
	return func(y *Yielder[T]) {
		y.fn = func(ctx context.Context) ([]T, error) {
			for {
				select {
				case <-ctx.Done():
					return nil, ErrStopped
				case val, ok := <-input:
					if !ok {
						return nil, ErrInputClosed
					}

					select {
					case <-ctx.Done():
						return nil, ErrStopped
					case y.genChan <- val:
					}
				}
			}
		}
	}
}

// New creates and starts a Yielder. It returns an error if the context is already
// done or if no generator function is provided. The yielder begins generating
// values immediately in a background goroutine.
func New[T comparable](ctx context.Context, opts ...Option[T]) (*Yielder[T], error) {
	y, err := newYielder(ctx, opts...)
	if err != nil {
		return nil, err
	}

	ctxWithCancel, cancel := context.WithTimeout(ctx, y.timeout)
	y.cancel = cancel

	go y.generate(ctxWithCancel)

	return y, nil
}

func newYielder[T comparable](ctx context.Context, opts ...Option[T]) (*Yielder[T], error) {
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

	y.genChan = make(chan T, y.buf)
	y.doneCh = make(chan struct{})

	return &y, nil
}

// Results return a read-only channel of generated values. The channel is closed
// when generation completes, is stopped, or the context is canceled.
func (yr *Yielder[T]) Results() <-chan T {
	return yr.genChan
}

// Err returns accumulated errors from generation. Multiple errors are joined via errors.Join.
func (yr *Yielder[T]) Err() error {
	return yr.err
}

// Done returns a channel closed when the yielder finishes, regardless of
// whether it completed successfully, errored, timed out, or was stopped.
func (yr *Yielder[T]) Done() <-chan struct{} {
	return yr.doneCh
}

func (yr *Yielder[T]) setErr(err error) {
	yr.onceStop.Do(func() { yr.err = err })
}

// Stop stops the yielder. It is safe to call multiple times.
func (yr *Yielder[T]) Stop() {
	defer yr.cancel()
	yr.setErr(ErrStopped)
}

// StopErr stops the yielder with the provided error non nil error.
// If err is nil, it does nothing. It is safe to call multiple times.
func (yr *Yielder[T]) StopErr(err error) {
	if err == nil {
		return
	}

	defer yr.cancel()
	yr.setErr(fmt.Errorf("%w - %w", ErrStopped, err))
}

func (yr *Yielder[T]) generate(ctx context.Context) {
	defer close(yr.doneCh)

	func(yr *Yielder[T]) {
		defer func() {
			if r := recover(); r != nil {
				yr.setErr(fmt.Errorf("panic in generator func: %+v", r))
			}

			yr.cancel()
			close(yr.genChan)
		}()

		values, errGen := yr.fn(ctx)
		if errGen != nil {
			yr.setErr(fmt.Errorf("err func generator: %w", errGen))
			return
		}

		for _, val := range values {
			select {
			case <-ctx.Done():
				yr.setErr(ErrStopped)
				return
			case yr.genChan <- val:
			}
		}
	}(yr)
}
