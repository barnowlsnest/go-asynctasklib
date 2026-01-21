package workflow

import (
	"context"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/barnowlsnest/go-asynctasklib/pkg/task"
)

type TaskGroup struct {
	sem       *Semaphore
	mu        sync.Mutex
	tasks     []*task.Task
	stopped   atomic.Bool
	releaseWg sync.WaitGroup
}

func NewTaskGroup(maxWorkers int) (*TaskGroup, error) {
	if maxWorkers <= 0 {
		return nil, ErrMaxWorkers
	}

	tg := TaskGroup{
		sem:   NewSemaphore(maxWorkers),
		tasks: make([]*task.Task, 0),
	}

	return &tg, nil
}

func (tg *TaskGroup) Submit(ctx context.Context, def task.Definition) (*task.Task, error) {
	if tg.stopped.Load() {
		return nil, ErrPoolStopped
	}

	t := task.New(def)

	// Add to the task list under lock
	tg.mu.Lock()
	tg.tasks = append(tg.tasks, t)
	tg.mu.Unlock()

	// Acquire semaphore slot (blocks until worker available)
	tg.sem.Acquire()

	// Start the task
	if err := t.Go(ctx); err != nil {
		tg.sem.Release()
		return t, err
	}

	// Release semaphore when the task completes
	tg.releaseWg.Go(func() {
		defer tg.sem.Release()
		t.Await()
	})

	return t, nil
}

func (tg *TaskGroup) SubmitWithErrGroup(ctx context.Context, defs []task.Definition) error {
	g, errCtx := errgroup.WithContext(ctx)

	for _, def := range defs {
		d := def
		g.Go(func() error {
			t, err := tg.Submit(errCtx, d)
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

func (tg *TaskGroup) SubmitBatch(ctx context.Context, defs []task.Definition) ([]*task.Task, error) {
	tasks := make([]*task.Task, 0, len(defs))

	for _, def := range defs {
		t, err := tg.Submit(ctx, def)
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, t)
	}

	return tasks, nil
}

func (tg *TaskGroup) Wait() {
	tg.mu.Lock()
	tasksCopy := make([]*task.Task, len(tg.tasks))
	copy(tasksCopy, tg.tasks)
	tg.mu.Unlock()

	wg := sync.WaitGroup{}
	for _, t := range tasksCopy {
		wg.Go(t.Await)
	}

	wg.Go(tg.releaseWg.Wait)
	wg.Wait()
}

func (tg *TaskGroup) WaitWithContext(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		tg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (tg *TaskGroup) Stop() {
	tg.stopped.Store(true)
}

func (tg *TaskGroup) StopWithContext(ctx context.Context) error {
	tg.Stop()

	tg.mu.Lock()
	tasksCopy := make([]*task.Task, len(tg.tasks))
	copy(tasksCopy, tg.tasks)
	tg.mu.Unlock()

	for _, t := range tasksCopy {
		if t.IsInProgress() {
			t.Cancel()
		}
	}

	return tg.WaitWithContext(ctx)
}

func (tg *TaskGroup) Tasks() []*task.Task {
	tg.mu.Lock()
	defer tg.mu.Unlock()

	tasksCopy := make([]*task.Task, len(tg.tasks))
	copy(tasksCopy, tg.tasks)
	return tasksCopy
}

// MaxWorkers returns the maximum number of concurrent workers.
func (tg *TaskGroup) MaxWorkers() int {
	return tg.sem.Limit()
}

// ActiveWorkers returns the current number of active workers.
func (tg *TaskGroup) ActiveWorkers() int {
	return tg.sem.Acquired()
}

// AvailableWorkers returns the number of available worker slots.
func (tg *TaskGroup) AvailableWorkers() int {
	return tg.sem.Available()
}

// IsStopped returns true if the pool has been stopped.
func (tg *TaskGroup) IsStopped() bool {
	return tg.stopped.Load()
}

// Stats returns statistics about the pool's tasks.
func (tg *TaskGroup) Stats() PoolStats {
	tg.mu.Lock()
	defer tg.mu.Unlock()

	stats := PoolStats{
		Total:         len(tg.tasks),
		MaxWorkers:    tg.MaxWorkers(),
		ActiveWorkers: tg.ActiveWorkers(),
		Stopped:       tg.IsStopped(),
	}

	for _, t := range tg.tasks {
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
