package workerpool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidConfig = errors.New("invalid config")

const defaultLeaveTimeout = time.Second

type (
	Config[T any] struct {
		Name             string
		Rate             float64
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
		limiter *rate.Limiter
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
	case c.Size <= 0:
		return fmt.Errorf("%w: size must be greater than 0", ErrInvalidConfig)
	case c.MaxSubmitRetries <= 0:
		return fmt.Errorf("%w: max submit retries must be greater than 0", ErrInvalidConfig)
	case c.Rate <= 0:
		return fmt.Errorf("%w: submit rate must be greater than 0", ErrInvalidConfig)
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
	pool := &WorkerPool[T]{
		claims:  claims,
		workers: make(map[uint64]*Worker[T], cfg.Size),
		cfg:     cfg,
		limiter: rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Size),
		ctx:     func() context.Context { return ctx },
		cancel:  cancel,
		handler: handlerFunc,
	}

	if err := pool.staffWorkers(); err != nil {
		pool.cancel()
		return nil, err
	}

	if err := pool.start(); err != nil {
		pool.cancel()
		return nil, err
	}

	return pool, nil
}

func (p *WorkerPool[T]) createWorker(id uint64) (*Worker[T], error) {
	return NewWorker[T](p.cfg.workerConfig(id, p.handler))
}

// staffWorkers creates cfg.Size workers and registers them in p.workers. It
// does not subscribe them to the claims source — that is start's job. Keeping
// creation separate from subscription makes it straightforward to later
// introduce a mode where only a subset of staffed workers is initially
// joined.
func (p *WorkerPool[T]) staffWorkers() error {
	for i := range p.cfg.Size {
		id := uint64(i + 1)
		w, err := p.createWorker(id)
		if err != nil {
			return err
		}

		p.workers[id] = w
	}

	return nil
}

// start Joins every staffed worker to the pool's claims source. On partial
// failure it Leaves the workers that were already successfully joined so the
// pool does not leak goroutines.
func (p *WorkerPool[T]) start() error {
	joined := make([]*Worker[T], 0, len(p.workers))
	for _, w := range p.workers {
		if err := w.Join(p.ctx(), p.claims); err != nil {
			for _, jw := range joined {
				_ = jw.Leave(p.claims, defaultLeaveTimeout)
			}

			return err
		}

		joined = append(joined, w)
	}

	return nil
}

func (p *WorkerPool[T]) Submit(job *T) (*Submission, error) {
	if err := p.limiter.Wait(p.ctx()); err != nil {
		return nil, err
	}

	return p.claims.Submit(p.ctx(), job), nil
}
