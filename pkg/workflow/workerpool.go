package workflow

import (
	"context"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

type (
	Pool struct {
		sem       *Semaphore
		mu        sync.Mutex
		tasks     []*task.Task
		stopped   atomic.Bool
		releaseWg sync.WaitGroup
	}

	PoolConfig struct {
		MaxWorkers int
		Hooks      *task.StateHooks
	}
)

func NewWorkerPool(maxWorkers int) *Pool {
	return NewWorkerPoolWithConfig(PoolConfig{
		MaxWorkers: maxWorkers,
	})
}

func NewWorkerPoolWithConfig(cfg PoolConfig) *Pool {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}

	return &Pool{
		sem:   NewSemaphore(cfg.MaxWorkers),
		tasks: make([]*task.Task, 0),
	}
}

func (p *Pool) Submit(ctx context.Context, def task.Definition) (*task.Task, error) {
	if p.stopped.Load() {
		return nil, ErrPoolStopped
	}

	t := task.New(def)

	// Add to the task list under lock
	p.mu.Lock()
	p.tasks = append(p.tasks, t)
	p.mu.Unlock()

	// Acquire semaphore slot (blocks until worker available)
	p.sem.Acquire()

	// Start the task
	if err := t.Go(ctx); err != nil {
		p.sem.Release()
		return t, err
	}

	// Release semaphore when task completes
	p.releaseWg.Go(func() {
		defer p.sem.Release()
		t.Await()
	})

	return t, nil
}

func (p *Pool) SubmitWithErrGroup(ctx context.Context, defs []task.Definition) error {
	g, errCtx := errgroup.WithContext(ctx)

	for _, def := range defs {
		d := def
		g.Go(func() error {
			t, err := p.Submit(errCtx, d)
			if err != nil {
				return err
			}

			t.Await()

			if t.IsFailed() {
				return t.Err()
			}

			return nil
		})
	}

	return g.Wait()
}

func (p *Pool) SubmitBatch(ctx context.Context, defs []task.Definition) ([]*task.Task, error) {
	tasks := make([]*task.Task, 0, len(defs))

	for _, def := range defs {
		t, err := p.Submit(ctx, def)
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, t)
	}

	return tasks, nil
}

func (p *Pool) Wait() {
	p.mu.Lock()
	tasksCopy := make([]*task.Task, len(p.tasks))
	copy(tasksCopy, p.tasks)
	p.mu.Unlock()

	for _, t := range tasksCopy {
		t.Await()
	}

	// Wait for all semaphore release goroutines to complete
	p.releaseWg.Wait()
}

func (p *Pool) WaitWithContext(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) Stop() {
	p.stopped.Store(true)
}

func (p *Pool) StopWithContext(ctx context.Context) error {
	p.Stop()

	p.mu.Lock()
	tasksCopy := make([]*task.Task, len(p.tasks))
	copy(tasksCopy, p.tasks)
	p.mu.Unlock()

	for _, t := range tasksCopy {
		if t.IsInProgress() {
			t.Cancel()
		}
	}

	return p.WaitWithContext(ctx)
}

func (p *Pool) Tasks() []*task.Task {
	p.mu.Lock()
	defer p.mu.Unlock()

	tasksCopy := make([]*task.Task, len(p.tasks))
	copy(tasksCopy, p.tasks)
	return tasksCopy
}

// MaxWorkers returns the maximum number of concurrent workers.
func (p *Pool) MaxWorkers() int {
	return p.sem.Limit()
}

// ActiveWorkers returns the current number of active workers.
func (p *Pool) ActiveWorkers() int {
	return p.sem.Acquired()
}

// AvailableWorkers returns the number of available worker slots.
func (p *Pool) AvailableWorkers() int {
	return p.sem.Available()
}

// IsStopped returns true if the pool has been stopped.
func (p *Pool) IsStopped() bool {
	return p.stopped.Load()
}

// Stats returns statistics about the pool's tasks.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := PoolStats{
		Total:         len(p.tasks),
		MaxWorkers:    p.MaxWorkers(),
		ActiveWorkers: p.ActiveWorkers(),
		Stopped:       p.IsStopped(),
	}

	for _, t := range p.tasks {
		switch {
		case t.IsCreated():
			stats.Created++
		case t.IsPending():
			stats.Pending++
		case t.IsStarted():
			stats.Started++
		case t.IsDone():
			stats.Done++
		case t.IsFailed():
			stats.Failed++
		case t.IsCanceled():
			stats.Canceled++
		}
	}

	return stats
}

type PoolStats struct {
	Total         int
	Created       int
	Pending       int
	Started       int
	Done          int
	Failed        int
	Canceled      int
	MaxWorkers    int
	ActiveWorkers int
	Stopped       bool
}
