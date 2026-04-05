package workerpool

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidConfig = errors.New("invalid config")

type (
	Config[T any] struct {
		Name             string
		Size             int
		MaxSubmitRetries int
		BaseRetryDelay   time.Duration
		MaxRetryDelay    time.Duration
		SubmitTimeout    time.Duration
		IdleTimeout      time.Duration
		Events           Events[T]
	}

	WorkerPool[T any] struct {
		claims  *Claims[T]
		cfg     *Config[T]
		workers map[uint64]*Worker[T]
		ctx     ContextFunc
		cancel  context.CancelFunc
		handler HandlerFunc[T]
	}

	ContextFunc func() context.Context
)

func (c *Config[T]) claimsConfig() *ClaimsConfig {
	return &ClaimsConfig{
		Name:             c.Name,
		Size:             c.Size,
		MaxSubmitRetries: c.MaxSubmitRetries,
		BaseRetryDelay:   c.BaseRetryDelay,
		MaxRetryDelay:    c.MaxRetryDelay,
		SubmitTimeout:    c.SubmitTimeout,
	}
}

func (c *Config[T]) workerConfig(id uint64, handlerFunc HandlerFunc[T]) *WorkerConfig[T] {
	return &WorkerConfig[T]{
		ID:          id,
		HandlerFunc: handlerFunc,
		IdleTimeout: c.IdleTimeout,
		Events:      c.Events,
	}
}

func (c *Config[T]) validate() error {
	switch {
	case c == nil:
		return fmt.Errorf("%w: nil config", ErrInvalidConfig)
	case c.Size < 0:
		return fmt.Errorf("%w: size must be positive", ErrInvalidConfig)
	case c.MaxSubmitRetries <= 0:
		return fmt.Errorf("%w: max submit retries must be greater than 0", ErrInvalidConfig)
	default:
		return nil
	}
}

func validateParentContext(parentCtx context.Context) error {
	switch {
	case parentCtx == nil:
		return ErrNilCtx
	case parentCtx.Err() != nil:
		return parentCtx.Err()
	default:
		return nil
	}
}

func New[T any](parentCtx context.Context, cfg *Config[T], handlerFunc HandlerFunc[T]) (*WorkerPool[T], error) {
	if err := validateParentContext(parentCtx); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	claims, err := NewClaims[T](cfg.claimsConfig())
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parentCtx)
	workers := make(map[uint64]*Worker[T], cfg.Size)
	pool := &WorkerPool[T]{
		claims:  claims,
		workers: workers,
		cfg:     cfg,
		ctx:     func() context.Context { return ctx },
		cancel:  func() { cancel() },
		handler: handlerFunc,
	}

	if err := pool.init(); err != nil {
		return nil, err
	}

	return pool, nil
}

func (p *WorkerPool[T]) createWorker(id uint64) (*Worker[T], error) {
	return NewWorker[T](p.ctx(), p.cfg.workerConfig(id, p.handler))
}

func (p *WorkerPool[T]) init() error {
	var err error
	for i := range p.cfg.Size {
		id := uint64(i + 1)
		if p.workers[id], err = p.createWorker(id); err != nil {
			return err
		}
	}

	return nil
}
