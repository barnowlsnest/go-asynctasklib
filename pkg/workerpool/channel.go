package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/pkg/retry"
	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

const (
	defaultMaxSize          = 4
	defaultMaxSubmitRetries = 6
	defaultBaseRetryDelay   = 500 * time.Millisecond
	defaultMaxRetryDelay    = 5 * time.Second
)

const submitJobTaskID = 1

type (
	Channel[T any] struct {
		mu               sync.RWMutex
		cfg              *ChannelConfig
		activeWorkersSet map[uint64]struct{}
		idleWorkersSet   map[uint64]struct{}
		claimsCh         JobClaims[T]
	}

	ChannelConfig struct {
		MaxSize          int
		MaxSubmitRetries int
		BaseRetryDelay   time.Duration
		MaxRetryDelay    time.Duration
	}

	Submission struct {
		mu     sync.Mutex
		err    error
		doneCh chan struct{}
		task   *task.Task
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

	return &Channel[T]{
		cfg:              cfg,
		claimsCh:         make(JobClaims[T], cfg.MaxSize),
		activeWorkersSet: make(map[uint64]struct{}, cfg.MaxSize),
		idleWorkersSet:   make(map[uint64]struct{}, cfg.MaxSize),
	}
}

func (ch *Channel[T]) send(job *T) error {
	c, ok := <-ch.claimsCh
	if !ok {
		return ErrDispatcherClosed
	}

	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.activeWorkersSet) == 0 {
		return ErrNoActiveWorkers
	}

	_, exists := ch.activeWorkersSet[c.id]
	if !exists {
		return fmt.Errorf("worker %d is not active", c.id)
	}

	c.jobCh <- job

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

func (ch *Channel[T]) Subscribe(w *Worker[T]) error {
	if w == nil {
		return ErrInvalidWorker
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	if len(ch.activeWorkersSet) >= ch.cfg.MaxSize {
		return ErrMaxPoolSize
	}

	ch.activeWorkersSet[w.ID()] = struct{}{}
	delete(ch.idleWorkersSet, w.ID())

	return nil
}

func (ch *Channel[T]) Unsubscribe(w *Worker[T]) {
	if w == nil {
		return
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	delete(ch.activeWorkersSet, w.ID())
	ch.idleWorkersSet[w.ID()] = struct{}{}
}

func (ch *Channel[T]) JobClaims() JobClaims[T] {
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

func (s *Submission) Done() <-chan struct{} {
	return s.doneCh
}

func (s *Submission) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *Submission) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Submission) Cancel() {
	switch {
	case s == nil:
		return
	case s.task == nil:
		return
	default:
		s.task.Cancel()
	}
}
