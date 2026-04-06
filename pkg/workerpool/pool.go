package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidConfig = errors.New("invalid config")

const defaultLeaveTimeout = time.Second

// PoolMode selects the worker lifecycle strategy.
type PoolMode int

const (
	// ModeFixed joins all workers at creation time and keeps them for the
	// pool's lifetime. This is the zero-value default.
	ModeFixed PoolMode = iota
	// ModeAutoScale starts at MinSize workers and dynamically scales
	// between MinSize and MaxSize based on claim-channel utilization.
	ModeAutoScale
)

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
		Mode             PoolMode
	}

	WorkerPool[T any] struct {
		claims       *Claims[T]
		cfg          *Config[T]
		limiter      *rate.Limiter
		mu           sync.Mutex
		active       map[uint64]*Worker[T]
		parked       map[uint64]*Worker[T]
		ctx          ContextFunc
		cancel       context.CancelFunc
		handler      HandlerFunc[T]
		shutdownOnce sync.Once
		shutdownErr  error
	}

	ContextFunc func() context.Context
)

func (c *Config[T]) claimsConfig() *ClaimsConfig {
	size := c.Size

	return &ClaimsConfig{
		Name:          c.Name,
		Size:          size,
		SubmitTimeout: c.SubmitTimeout,
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
	case c.MaxSubmitRetries <= 0:
		return fmt.Errorf("%w: max submit retries must be greater than 0", ErrInvalidConfig)
	case c.Rate <= 0:
		return fmt.Errorf("%w: submit rate must be greater than 0", ErrInvalidConfig)
	}

	switch c.Mode {
	case ModeFixed:
		if c.Size <= 0 {
			return fmt.Errorf("%w: size must be greater than 0", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: unknown pool mode %d", ErrInvalidConfig, c.Mode)
	}

	return nil
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
		active:  make(map[uint64]*Worker[T]),
		parked:  make(map[uint64]*Worker[T]),
		cfg:     cfg,
		limiter: rate.NewLimiter(rate.Limit(cfg.Rate), cfg.claimsConfig().Size),
		ctx:     func() context.Context { return ctx },
		cancel:  cancel,
		handler: handlerFunc,
	}

	if err := pool.staffWorkers(); err != nil {
		pool.cancel()
		return nil, err
	}

	return pool, nil
}

func (p *WorkerPool[T]) createWorker(id uint64) (*Worker[T], error) {
	return NewWorker[T](p.cfg.workerConfig(id, p.handler))
}

func (p *WorkerPool[T]) staffWorkers() error {
	var totalWorkers int
	var initialJoin int

	switch p.cfg.Mode {
	case ModeFixed:
		totalWorkers = p.cfg.Size
		initialJoin = p.cfg.Size
	default:
		return fmt.Errorf("unsupported mode: %v", p.cfg.Mode)
	}

	workers := make([]*Worker[T], 0, totalWorkers)
	for i := range totalWorkers {
		id := uint64(i + 1)
		w, err := p.createWorker(id)
		if err != nil {
			return err
		}
		workers = append(workers, w)
	}

	// Join the initial set.
	joined := make([]*Worker[T], 0, initialJoin)
	for _, w := range workers[:initialJoin] {
		if err := w.Join(p.ctx(), p.claims); err != nil {
			for _, jw := range joined {
				_ = jw.Leave(p.claims, defaultLeaveTimeout)
			}
			return err
		}
		joined = append(joined, w)
		p.active[w.ID()] = w
	}

	// Park the remainder (AutoScale only).
	for _, w := range workers[initialJoin:] {
		p.parked[w.ID()] = w
	}

	return nil
}

func (p *WorkerPool[T]) activeWorkers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.active)
}

func (p *WorkerPool[T]) Shutdown(timeout time.Duration) error {
	p.shutdownOnce.Do(func() {
		// Cancel the pool context before leaving workers. This unblocks
		// any in-flight send goroutines (they select on ctx.Done) so they
		// stop referencing worker input channels before Close() is called.
		p.cancel()

		// Snapshot active workers under lock.
		p.mu.Lock()
		snapshot := make([]*Worker[T], 0, len(p.active))
		for _, w := range p.active {
			snapshot = append(snapshot, w)
		}
		p.mu.Unlock()

		// Leave all active workers in parallel, outside the lock.
		perWorker := timeout
		if len(snapshot) > 0 {
			perWorker = timeout / time.Duration(len(snapshot))
			perWorker = max(perWorker, defaultLeaveTimeout)
		}

		var (
			mu   sync.Mutex
			errs []error
			wg   sync.WaitGroup
		)

		for _, w := range snapshot {
			wg.Go(func() {
				if err := w.Leave(p.claims, perWorker); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
		wg.Wait()

		// Move all to parked.
		p.mu.Lock()
		for _, w := range snapshot {
			delete(p.active, w.ID())
			p.parked[w.ID()] = w
		}
		p.mu.Unlock()

		p.shutdownErr = errors.Join(errs...)
	})

	return p.shutdownErr
}

func (p *WorkerPool[T]) Submit(job *T) (<-chan error, error) {
	if err := p.ctx().Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPoolShutdown, err)
	}

	if err := p.limiter.Wait(p.ctx()); err != nil {
		return nil, err
	}

	errCh := make(chan error)
	go func() {
		defer close(errCh)
		errCh <- p.claims.Submit(p.ctx(), job)
	}()

	return errCh, nil
}
