package workerpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/barnowlsnest/go-asynctasklib/pkg/retry"
	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

const (
	defaultMaxSize          = 10
	defaultMaxSubmitRetries = 3
	defaultBaseRetryDelay   = time.Second
	defaultMaxRetryDelay    = 10 * time.Second
	defaultRPS              = 100
	defaultBurst            = 5
)

const submitJobTaskID = 1

type (
	Labor[T any] struct {
		mu               sync.RWMutex
		cfg              *LaborConfig
		activeWorkersSet map[uint64]struct{}
		idleWorkersSet   map[uint64]struct{}
		dispatcher       Dispatcher[T]
	}

	LaborConfig struct {
		MaxSize          int
		MaxSubmitRetries int
		BaseRetryDelay   time.Duration
		MaxRetryDelay    time.Duration
		RateLimit        *rate.Limiter
	}
)

func NewLabor[T any](cfg *LaborConfig) *Labor[T] {
	if cfg == nil {
		cfg = &LaborConfig{
			MaxSize:          defaultMaxSize,
			MaxSubmitRetries: defaultMaxSubmitRetries,
			BaseRetryDelay:   defaultBaseRetryDelay,
			MaxRetryDelay:    defaultMaxRetryDelay,
			RateLimit:        rate.NewLimiter(defaultRPS, defaultBurst),
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

	if cfg.RateLimit == nil {
		cfg.RateLimit = rate.NewLimiter(defaultRPS, defaultBurst)
	}

	return &Labor[T]{
		cfg:              cfg,
		dispatcher:       make(Dispatcher[T], cfg.MaxSize),
		activeWorkersSet: make(map[uint64]struct{}, cfg.MaxSize),
		idleWorkersSet:   make(map[uint64]struct{}, cfg.MaxSize),
	}
}

func (l *Labor[T]) submitJob(job *T) error {
	claim, ok := <-l.dispatcher
	if !ok {
		return ErrDispatcherClosed
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.activeWorkersSet) == 0 {
		return ErrNoActiveWorkers
	}

	_, exists := l.activeWorkersSet[claim.ID]
	if !exists {
		return fmt.Errorf("worker %d is not active", claim.ID)
	}

	claim.Job <- job

	return nil
}

func (l *Labor[T]) newRetriableSubmit(job *T) (*task.Task, error) {
	if job == nil {
		return nil, ErrNilJob
	}

	cfg := l.cfg
	retrier := retry.NewExponentialBackoff(
		retry.WithBaseDelay(cfg.BaseRetryDelay),
		retry.WithMaxDelay(cfg.MaxRetryDelay),
		retry.WithJitter(true),
	)

	def, err := task.NewBuilder(
		task.WithID(submitJobTaskID),
		task.WithMaxRetries(cfg.MaxSubmitRetries),
		task.WithRetryStrategy(retrier),
		task.WithTaskFn(func(_ *task.Run) error { return l.submitJob(job) }),
	).Build()

	if err != nil {
		return nil, err
	}

	return task.New(*def), nil
}

func (l *Labor[T]) Subscribe(w *Worker[T]) error {
	if w == nil {
		return ErrInvalidWorker
	}

	if len(l.activeWorkersSet) == l.cfg.MaxSize {
		return ErrMaxPoolSize
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.activeWorkersSet[w.ID()] = struct{}{}
	delete(l.idleWorkersSet, w.ID())

	return nil
}

func (l *Labor[T]) Unsubscribe(w *Worker[T]) {
	if w == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.activeWorkersSet, w.ID())
	l.idleWorkersSet[w.ID()] = struct{}{}
}

func (l *Labor[T]) Jobs() Dispatcher[T] {
	return l.dispatcher
}

func (l *Labor[T]) Submit(ctx context.Context, job *T) error {
	if err := l.cfg.RateLimit.Wait(ctx); err != nil {
		return err
	}

	submit, err := l.newRetriableSubmit(job)
	if err != nil {
		return err
	}

	return submit.GoRetry(ctx)
}
