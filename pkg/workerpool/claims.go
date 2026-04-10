package workerpool

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

var (
	// ErrMaxPoolSize is returned from Claims.Subscribe when the number of
	// subscribers would exceed ClaimsConfig.Size.
	ErrMaxPoolSize = errors.New("max pool size exceeded")
	// ErrDispatcherClosed is returned from Claims.Submit when the inbound
	// claims channel has been closed.
	ErrDispatcherClosed = errors.New("dispatcher closed")
	// ErrNoWorkers is returned from Claims.Submit when SubmitTimeout
	// elapses while no workers are subscribed to the dispatcher.
	ErrNoWorkers = errors.New("no active workers")
	// ErrSubmitTimeout is returned from Claims.Submit when workers are
	// subscribed but none of them accepted the job within SubmitTimeout.
	ErrSubmitTimeout = errors.New("worker input write timeout")
)

const (
	defaultSubmitBackoff        = 50 * time.Millisecond
	defaultSubmitTimeout        = time.Second
	defaultSubmitAttemptsPerSec = 5
	defaultBackoffFactor        = 1.5
)

type (
	// Claims is the lock-coordinated fan-out dispatcher that backs a
	// WorkerPool. Workers Subscribe to advertise availability by pushing
	// their input channel onto claimsCh, and the pool (or any caller)
	// invokes Submit to hand a job to the next advertised worker.
	Claims[T any] struct {
		mu          sync.Mutex
		cfg         *ClaimsConfig
		subscribers map[uint64]WorkerCloser[T]
		claimsCh    chan *Claim[T]
	}

	// ClaimsConfig controls Claims construction and Submit behavior.
	// Zero-valued fields fall back to package defaults via applyDefaults.
	ClaimsConfig struct {
		// Name is an optional label surfaced via Claims.Name and the
		// Worker.String debug format.
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

	// Claim is a worker's self-advertisement: an (id, input) pair that
	// tells the dispatcher which worker owns the input channel.
	Claim[T any] struct {
		id    uint64
		input chan *T
	}

	// WorkerCloser is the minimum interface a Claims subscriber must
	// satisfy: it must expose a stable numeric identity.
	WorkerCloser[T any] interface {
		ID() uint64
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

// NewClaims constructs a Claims dispatcher from cfg. It returns ErrNil
// if cfg is nil. Missing fields on cfg are populated with package
// defaults before construction.
func NewClaims[T any](cfg *ClaimsConfig) (*Claims[T], error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil claims config", ErrNil)
	}

	cfg.applyDefaults()

	return &Claims[T]{
		cfg:         cfg,
		claimsCh:    make(chan *Claim[T], cfg.Size),
		subscribers: make(map[uint64]WorkerCloser[T], cfg.Size),
	}, nil
}

// Subscribe registers w as an active worker. It returns ErrInvalidWorker
// if w is nil, or ErrMaxPoolSize if the dispatcher already has Size
// subscribers. Subscribing the same ID twice overwrites the previous
// entry and is not treated as an error.
func (c *Claims[T]) Subscribe(w WorkerCloser[T]) error {
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
func (c *Claims[T]) Unsubscribe(w WorkerCloser[T]) error {
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

// Submit hands job to the next advertised worker. It blocks until a
// worker accepts the job, ctx is canceled, or SubmitTimeout elapses.
//
// Submit returns:
//   - ctx.Err() if ctx is canceled or deadline-exceeded
//   - ErrDispatcherClosed if the inbound claims channel is closed
//   - ErrNoWorkers if SubmitTimeout elapses and no workers are subscribed
//   - ErrSubmitTimeout if SubmitTimeout elapses but workers are subscribed
//     (their input channels were all unresponsive: this is the "stale
//     claim" backoff path)
func (c *Claims[T]) Submit(ctx context.Context, job *T) error {
	begin := time.Now()
	backoff := c.cfg.SubmitBackoff
	maxBackoff := time.Second / time.Duration(c.cfg.SubmitAttemptsPerSec)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.SubmitTimeout):
			c.mu.Lock()
			subs := len(c.subscribers)
			c.mu.Unlock()
			if subs == 0 {
				return ErrNoWorkers
			}
			return ErrSubmitTimeout
		case worker, ok := <-c.claimsCh:
			if !ok {
				return ErrDispatcherClosed
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case worker.input <- job:
				return nil
			default:
				if time.Since(begin) >= c.cfg.SubmitTimeout {
					c.mu.Lock()
					subs := len(c.subscribers)
					c.mu.Unlock()
					if subs == 0 {
						return ErrNoWorkers
					}
				}

				remaining := c.cfg.SubmitTimeout - time.Since(begin)
				sleep := backoff
				if sleep > remaining {
					sleep = remaining
				}

				time.Sleep(sleep)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}

				continue
			}
		}
	}
}

// Name returns the optional label set via ClaimsConfig.Name.
func (c *Claims[T]) Name() string {
	return c.cfg.Name
}

// Claims exposes the inbound advertisement channel so workers can publish
// their availability. It satisfies the Jobs[T] interface used by Worker.Join.
func (c *Claims[T]) Claims() chan *Claim[T] {
	return c.claimsCh
}

// Size returns the configured maximum number of subscribers.
func (c *Claims[T]) Size() int {
	return c.cfg.Size
}

// PendingClaims returns the number of workers currently parked on the
// claims channel advertising themselves as idle.
func (c *Claims[T]) PendingClaims() int { return len(c.claimsCh) }
