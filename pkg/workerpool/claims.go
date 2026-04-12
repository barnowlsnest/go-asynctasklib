package workerpool

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

var (
	// ErrMaxPoolSize is returned from claims.Subscribe when the number of
	// subscribers would exceed ClaimsConfig.Size.
	ErrMaxPoolSize = errors.New("max pool size exceeded")
	// ErrDispatcherClosed is returned from claims.Submit when the inbound
	// claims channel has been closed.
	ErrDispatcherClosed = errors.New("dispatcher closed")
	// ErrNoWorkers is returned from claims.Submit when SubmitTimeout
	// elapses while no workers are subscribed to the dispatcher.
	ErrNoWorkers = errors.New("no active workers")
	// ErrSubmitTimeout is returned from claims.Submit when workers are
	// subscribed, but none of them accepted the job within SubmitTimeout.
	ErrSubmitTimeout = errors.New("worker input write timeout")
)

const (
	defaultSubmitBackoff        = 50 * time.Millisecond
	defaultSubmitTimeout        = time.Second
	defaultSubmitAttemptsPerSec = 5
	defaultBackoffFactor        = 1.5
)

type (
	Subscriber[T any] interface {
		ID() uint64
	}

	// claims is the lock-coordinated fan-out dispatcher that backs a
	// WorkerPool. Workers Subscribe to advertise availability by pushing
	// their input channel onto claimsCh, and the pool (or any caller)
	// invokes submit to hand a job to the next advertised worker.
	claims[T any] struct {
		mu          sync.Mutex
		cfg         *ClaimsConfig
		subscribers map[uint64]Subscriber[T]
		claimsCh    chan *claim[T]
	}

	// ClaimsConfig controls claims construction and Submit behavior.
	// Zero-valued fields fall back to package defaults via applyDefaults.
	ClaimsConfig struct {
		// Name is an optional label surfaced via claims.Name and the
		// worker.String debug format.
		Name string
		// BackoffFactor multiplies SubmitBackoff on every retry of a
		// stale claim. Must be in (0, 1]; values outside that range
		// reset to the package default.
		BackoffFactor float64
		// Size is the maximum number of workers that may Subscribe. It
		// also sizes the buffered claimsCh so each worker can park an
		// advertisement without contention. Defaults to runtime.NumCPU.
		Size int
		// SubmitAttemptsPerSec caps the retry rate for stale claims.
		SubmitAttemptsPerSec int
		// SubmitBackoff is the initial backoff between retries after a
		// stale claim is pulled.
		SubmitBackoff time.Duration
		// SubmitTimeout bounds how long Submit will wait for a worker to
		// accept a job before returning ErrSubmitTimeout or ErrNoWorkers.
		SubmitTimeout time.Duration
	}

	// claim is a worker's self-advertisement: an (id, input) pair that
	// tells the dispatcher which worker owns the input channel.
	claim[T any] struct {
		id    uint64
		input chan JobAware[T]
	}
)

func (cfg *ClaimsConfig) applyDefaults() {
	if cfg.Size <= 0 {
		cfg.Size = runtime.NumCPU()
	}

	if cfg.SubmitBackoff == time.Duration(0) {
		cfg.SubmitBackoff = defaultSubmitBackoff
	}

	if cfg.SubmitTimeout == time.Duration(0) {
		cfg.SubmitTimeout = defaultSubmitTimeout
	}

	if cfg.SubmitAttemptsPerSec <= 0 {
		cfg.SubmitAttemptsPerSec = defaultSubmitAttemptsPerSec
	}

	if cfg.BackoffFactor <= 0 || cfg.BackoffFactor > 1 {
		cfg.BackoffFactor = defaultBackoffFactor
	}
}

// newClaims constructs a claims dispatcher from cfg. It returns ErrNil
// if cfg is nil. Missing fields on cfg are populated with package
// defaults before construction.
func newClaims[T any](cfg *ClaimsConfig) (*claims[T], error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil claims config", ErrNil)
	}

	cfg.applyDefaults()

	return &claims[T]{
		cfg:         cfg,
		claimsCh:    make(chan *claim[T], cfg.Size),
		subscribers: make(map[uint64]Subscriber[T], cfg.Size),
	}, nil
}

// Subscribe registers w as an active worker. It returns ErrInvalidWorker
// if w is nil, or ErrMaxPoolSize if the dispatcher already has Size
// subscribers. Subscribing the same ID twice overwrites the previous
// entry and is not treated as an error.
func (c *claims[T]) Subscribe(w Subscriber[T]) error {
	if w == nil {
		return ErrInvalidWorker
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.subscribers) >= c.cfg.Size {
		return ErrMaxPoolSize
	}

	c.subscribers[w.ID()] = w

	return nil
}

// Unsubscribe removes w from the active worker set. It is a no-op (and
// returns nil) if w is nil or was not previously subscribed.
func (c *claims[T]) Unsubscribe(w Subscriber[T]) error {
	if w == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	_, exists := c.subscribers[w.ID()]
	if !exists {
		return nil
	}

	delete(c.subscribers, w.ID())

	return nil
}

// submit hands job to the next advertised worker. It blocks until a
// worker accepts the job, ctx is canceled, or SubmitTimeout elapses.
//
// submit returns:
//   - ctx.Err() if ctx is canceled or deadline-exceeded
//   - ErrDispatcherClosed if the inbound claims channel is closed
//   - ErrNoWorkers if SubmitTimeout elapses and no workers are subscribed
//   - ErrSubmitTimeout if SubmitTimeout elapses but workers are subscribed
//     (their input channels were all unresponsive: scheduler-race window
//     or workers busy inside handlers)
func (c *claims[T]) submit(ctx JobAware[T]) error {
	deadline := time.Now().Add(c.cfg.SubmitTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.mu.Lock()
			subs := len(c.subscribers)
			c.mu.Unlock()
			if subs == 0 {
				return ErrNoWorkers
			}
			return ErrSubmitTimeout
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(remaining):
			continue
		case worker, ok := <-c.claimsCh:
			if !ok {
				return ErrDispatcherClosed
			}

			c.mu.Lock()
			_, alive := c.subscribers[worker.id]
			c.mu.Unlock()
			if !alive {
				continue
			}

			sendWait := c.cfg.SubmitBackoff
			if r := time.Until(deadline); r < sendWait {
				sendWait = r
			}

			if sendWait <= 0 {
				continue
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case worker.input <- ctx:
				return nil
			case <-time.After(sendWait):
				continue
			}
		}
	}
}

// Name returns the optional label set via ClaimsConfig.Name.
func (c *claims[T]) Name() string {
	return c.cfg.Name
}

// Claims exposes the inbound advertisement channel so workers can publish
// their availability. It satisfies the jobs[T] interface used by worker.Join.
func (c *claims[T]) Claims() chan *claim[T] {
	return c.claimsCh
}

// Size returns the configured maximum number of subscribers.
func (c *claims[T]) Size() int {
	return c.cfg.Size
}

// PendingClaims returns the number of workers currently parked on the
// claims channel advertising themselves as idle.
func (c *claims[T]) PendingClaims() int { return len(c.claimsCh) }
