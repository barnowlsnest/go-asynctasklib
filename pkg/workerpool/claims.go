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
	ErrMaxPoolSize      = errors.New("max pool size exceeded")
	ErrDispatcherClosed = errors.New("dispatcher closed")
	ErrNoWorkers        = errors.New("no active workers")
	ErrSubmitTimeout    = errors.New("worker input write timeout")
)

const (
	defaultSubmitBackoff        = 50 * time.Millisecond
	defaultSubmitTimeout        = time.Second
	defaultSubmitAttemptsPerSec = 5
)

type (
	Claims[T any] struct {
		mu          sync.Mutex
		cfg         *ClaimsConfig
		subscribers map[uint64]WorkerCloser[T]
		claimsCh    chan *Claim[T]
	}

	ClaimsConfig struct {
		Name                 string
		Size                 int
		SubmitAttemptsPerSec int
		SubmitBackoff        time.Duration
		SubmitTimeout        time.Duration
	}

	Claim[T any] struct {
		id    uint64
		input chan *T
	}

	WorkerCloser[T any] interface {
		ID() uint64
	}
)

func NewClaims[T any](cfg *ClaimsConfig) (*Claims[T], error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil claims config", ErrNil)
	}

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

	return &Claims[T]{
		cfg:         cfg,
		claimsCh:    make(chan *Claim[T], cfg.Size),
		subscribers: make(map[uint64]WorkerCloser[T], cfg.Size),
	}, nil
}

func (c *Claims[T]) check(worker *Claim[T]) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.subscribers) == 0 {
		return ErrNoWorkers
	}

	_, exists := c.subscribers[worker.id]
	if !exists {
		return fmt.Errorf("worker %d is not subscribed to %q", worker.id, c.cfg.Name)
	}

	return nil
}

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

func (c *Claims[T]) Claims() chan *Claim[T] {
	return c.claimsCh
}

func (c *Claims[T]) Submit(ctx context.Context, job *T) error {
	begin := time.Now()
	backoff := c.cfg.SubmitBackoff
	maxBackoff := time.Second / time.Duration(c.cfg.SubmitAttemptsPerSec)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case worker, ok := <-c.claimsCh:
			if !ok {
				return ErrDispatcherClosed
			}
			if err := c.check(worker); err != nil {
				return err
			}
			select {
			case <-time.After(c.cfg.SubmitTimeout):
				return ErrSubmitTimeout
			case <-ctx.Done():
				return ctx.Err()
			case worker.input <- job:
				return nil
			}
		default:
			if time.Since(begin) >= c.cfg.SubmitTimeout {
				c.mu.Lock()
				subs := len(c.subscribers)
				c.mu.Unlock()
				switch subs {
				case 0:
					return ErrNoWorkers
				default:
					return ErrSubmitTimeout
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

func (c *Claims[T]) Name() string {
	return c.cfg.Name
}

func (c *Claims[T]) Size() int {
	return c.cfg.Size
}

// PendingClaims returns the number of workers currently parked on the
// claims channel advertising themselves as idle.
func (c *Claims[T]) PendingClaims() int { return len(c.claimsCh) }
