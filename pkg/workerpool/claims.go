package workerpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/pkg/retry"
	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

const (
	defaultMaxSize          = 100
	defaultMaxSubmitRetries = 5
	defaultBaseRetryDelay   = 500 * time.Millisecond
	defaultMaxRetryDelay    = 3 * time.Second
)

const submitJobTaskID = 1

var (
	ErrMaxPoolSize      = errors.New("max pool size exceeded")
	ErrDispatcherClosed = errors.New("dispatcher closed")
	ErrNoWorkers        = errors.New("no active workers")
)

type (
	Claims[T any] struct {
		mu          sync.Mutex
		cfg         *ClaimsConfig
		subscribers map[uint64]WorkerCloser[T]
		claimsCh    chan *Claim[T]
	}

	ClaimsConfig struct {
		Name             string
		Size             int
		MaxSubmitRetries int
		BaseRetryDelay   time.Duration
		MaxRetryDelay    time.Duration
		SubmitTimeout    time.Duration
	}

	Claim[T any] struct {
		id    uint64
		input chan *T
	}

	WorkerCloser[T any] interface {
		io.Closer
		ID() uint64
	}
)

func NewClaims[T any](cfg *ClaimsConfig) (*Claims[T], error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil claims config", ErrNil)
	}

	if cfg.Size <= 0 {
		cfg.Size = defaultMaxSize
	}

	if cfg.BaseRetryDelay == time.Duration(0) {
		cfg.BaseRetryDelay = defaultBaseRetryDelay
	}

	if cfg.MaxRetryDelay == time.Duration(0) {
		cfg.MaxRetryDelay = defaultMaxRetryDelay
	}

	if cfg.MaxSubmitRetries <= 0 {
		cfg.MaxSubmitRetries = defaultMaxSubmitRetries
	}

	return &Claims[T]{
		cfg:         cfg,
		claimsCh:    make(chan *Claim[T], cfg.Size),
		subscribers: make(map[uint64]WorkerCloser[T], cfg.Size),
	}, nil
}

func (c *Claims[T]) send(job *T) error {
	worker, ok := <-c.claimsCh
	if !ok {
		return ErrDispatcherClosed
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.subscribers) == 0 {
		return ErrNoWorkers
	}

	_, exists := c.subscribers[worker.id]
	if !exists {
		return fmt.Errorf("worker %d is not subscribed to %q", worker.id, c.cfg.Name)
	}

	worker.input <- job

	return nil
}

func (c *Claims[T]) newRetriableSubmit(job *T) (*task.Task, error) {
	if job == nil {
		return nil, ErrNilJob
	}

	cfg := c.cfg
	retrier := retry.NewExponentialBackoff(
		retry.WithBaseDelay(cfg.BaseRetryDelay),
		retry.WithMaxDelay(cfg.MaxRetryDelay),
		retry.WithJitter(true),
	)

	b := task.NewBuilder(
		task.WithID(submitJobTaskID),
		task.WithMaxDuration(cfg.SubmitTimeout),
		task.WithMaxRetries(cfg.MaxSubmitRetries),
		task.WithRetryStrategy(retrier),
		task.WithTaskFn(func(r *task.Run) error {
			err := c.send(job)
			if errors.Is(err, ErrDispatcherClosed) {
				r.Cancel()
			}

			return err
		}),
	)

	def, err := b.Build()
	if err != nil {
		return nil, err
	}

	return task.New(*def), nil
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
	sub, exists := c.subscribers[w.ID()]
	if !exists {
		return nil
	}

	if err := sub.Close(); err != nil {
		return err
	}

	delete(c.subscribers, w.ID())

	return nil
}

func (c *Claims[T]) Claims() chan *Claim[T] {
	return c.claimsCh
}

func (c *Claims[T]) Submit(ctx context.Context, job *T) *Submission {
	var s Submission
	s.task, s.err = c.newRetriableSubmit(job)
	if s.err != nil {
		s.doneCh = make(chan struct{})
		close(s.doneCh)
		return &s
	}

	s.doneCh = make(chan struct{})
	go func() {
		defer close(s.doneCh)
		if err := s.task.GoRetry(ctx); err != nil {
			s.setErr(err)
		}
	}()

	return &s
}

func (c *Claims[T]) Name() string {
	return c.cfg.Name
}

func (c *Claims[T]) Size() int {
	return c.cfg.Size
}
