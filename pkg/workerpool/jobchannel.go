package workerpool

import (
	"context"
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
	jobChannel[T any] struct {
		mu               sync.RWMutex
		cfg              *JobChannelConfig
		activeWorkersSet map[uint64]struct{}
		idleWorkersSet   map[uint64]struct{}
		dispatcher       dispatcher[T]
	}

	JobChannelConfig struct {
		MaxSize          int
		MaxSubmitRetries int
		BaseRetryDelay   time.Duration
		MaxRetryDelay    time.Duration
	}
)

func newJobChannel[T any](cfg *JobChannelConfig) *jobChannel[T] {
	if cfg == nil {
		cfg = &JobChannelConfig{
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

	return &jobChannel[T]{
		cfg:              cfg,
		dispatcher:       make(dispatcher[T], cfg.MaxSize),
		activeWorkersSet: make(map[uint64]struct{}, cfg.MaxSize),
		idleWorkersSet:   make(map[uint64]struct{}, cfg.MaxSize),
	}
}

func (l *jobChannel[T]) submitJob(job *T) error {
	c, ok := <-l.dispatcher
	if !ok {
		return ErrDispatcherClosed
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.activeWorkersSet) == 0 {
		return ErrNoActiveWorkers
	}

	_, exists := l.activeWorkersSet[c.id]
	if !exists {
		return fmt.Errorf("worker %d is not active", c.id)
	}

	c.jobCh <- job

	return nil
}

func (l *jobChannel[T]) newRetriableSubmit(job *T) (*task.Task, error) {
	if job == nil {
		return nil, ErrNilJob
	}

	cfg := l.cfg
	retrier := retry.NewExponentialBackoff(
		retry.WithBaseDelay(cfg.BaseRetryDelay),
		retry.WithMaxDelay(cfg.MaxRetryDelay),
		retry.WithJitter(true),
	)

	b := task.NewBuilder(
		task.WithID(submitJobTaskID),
		task.WithMaxRetries(cfg.MaxSubmitRetries),
		task.WithRetryStrategy(retrier),
		task.WithTaskFn(func(_ *task.Run) error { return l.submitJob(job) }),
	)

	def, err := b.Build()

	if err != nil {
		return nil, err
	}

	return task.New(*def), nil
}

func (l *jobChannel[T]) subscribe(w *Worker[T]) error {
	if w == nil {
		return ErrInvalidWorker
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.activeWorkersSet) >= l.cfg.MaxSize {
		return ErrMaxPoolSize
	}

	l.activeWorkersSet[w.ID()] = struct{}{}
	delete(l.idleWorkersSet, w.ID())

	return nil
}

func (l *jobChannel[T]) unsubscribe(w *Worker[T]) {
	if w == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.activeWorkersSet, w.ID())
	l.idleWorkersSet[w.ID()] = struct{}{}
}

func (l *jobChannel[T]) jobs() dispatcher[T] {
	return l.dispatcher
}

func (l *jobChannel[T]) submit(ctx context.Context, job *T) error {
	submit, err := l.newRetriableSubmit(job)
	if err != nil {
		return err
	}

	return submit.GoRetry(ctx)
}
