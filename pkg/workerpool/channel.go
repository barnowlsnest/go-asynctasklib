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

type (
	Channel[T any] struct {
		mu          sync.Mutex
		cfg         *ChannelConfig
		subscribers map[uint64]WorkerCloser[T]
		claimsCh    chan *Claim[T]
	}

	ChannelConfig struct {
		Name             string
		MaxSize          int
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

func NewChannel[T any](cfg *ChannelConfig) *Channel[T] {
	if cfg == nil {
		cfg = &ChannelConfig{
			MaxSize:          defaultMaxSize,
			MaxSubmitRetries: defaultMaxSubmitRetries,
			BaseRetryDelay:   defaultBaseRetryDelay,
			MaxRetryDelay:    defaultMaxRetryDelay,
		}
	}

	if cfg.MaxSize <= 0 {
		cfg.MaxSize = defaultMaxSize
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

	return &Channel[T]{
		cfg:         cfg,
		claimsCh:    make(chan *Claim[T], cfg.MaxSize),
		subscribers: make(map[uint64]WorkerCloser[T], cfg.MaxSize),
	}
}

func (ch *Channel[T]) send(job *T) error {
	c, ok := <-ch.claimsCh
	if !ok {
		return ErrDispatcherClosed
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	if len(ch.subscribers) == 0 {
		return ErrNoWorkers
	}

	_, exists := ch.subscribers[c.id]
	if !exists {
		return fmt.Errorf("worker %d is not subscribed to %q", c.id, ch.cfg.Name)
	}

	c.input <- job

	return nil
}

func (ch *Channel[T]) newRetriableSubmit(job *T) (*task.Task, error) {
	if job == nil {
		return nil, ErrNilJob
	}

	cfg := ch.cfg
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
			err := ch.send(job)
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

func (ch *Channel[T]) Subscribe(w WorkerCloser[T]) error {
	if w == nil {
		return ErrInvalidWorker
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	if len(ch.subscribers) >= ch.cfg.MaxSize {
		return ErrMaxPoolSize
	}

	ch.subscribers[w.ID()] = w

	return nil
}

func (ch *Channel[T]) Unsubscribe(w WorkerCloser[T]) error {
	if w == nil {
		return nil
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()
	sub, exists := ch.subscribers[w.ID()]
	if !exists {
		return nil
	}

	if err := sub.Close(); err != nil {
		return err
	}

	delete(ch.subscribers, w.ID())

	return nil
}

func (ch *Channel[T]) Claims() chan *Claim[T] {
	return ch.claimsCh
}

func (ch *Channel[T]) Submit(ctx context.Context, job *T) *Submission {
	var s Submission
	s.task, s.err = ch.newRetriableSubmit(job)
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

func (ch *Channel[T]) Name() string {
	return ch.cfg.Name
}

func (ch *Channel[T]) MaxSize() int {
	return ch.cfg.MaxSize
}
