package taskqueue

import (
	"context"
	"sync"
	"time"
)

// DeadTask is a task that exhausted its delivery attempts, with the failure
// that sent it to the dead-letter queue.
type DeadTask struct {
	Task     Task
	Reason   error
	Attempts int
	DeadAt   time.Time
}

// DeadLetter stores tasks that exhausted their delivery attempts. It is an
// independent seam from Backend so it can target a different store.
type DeadLetter interface {
	Add(ctx context.Context, task Task, reason error) error
	List(ctx context.Context, limit int) ([]DeadTask, error)
	Remove(ctx context.Context, id uint64) error
	Len(ctx context.Context) (int, error)
}

// MemoryDeadLetter is the default in-memory DeadLetter, keyed by task ID.
type MemoryDeadLetter struct {
	mu    sync.Mutex
	tasks map[uint64]DeadTask
}

// NewMemoryDeadLetter returns an empty in-memory dead-letter queue.
func NewMemoryDeadLetter() *MemoryDeadLetter {
	return &MemoryDeadLetter{tasks: make(map[uint64]DeadTask)}
}

func (d *MemoryDeadLetter) Add(ctx context.Context, task Task, reason error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task == nil {
		return ErrTaskNil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tasks[task.ID()] = DeadTask{
		Task:   task,
		Reason: reason,
		DeadAt: time.Now().UTC(),
	}
	return nil
}

func (d *MemoryDeadLetter) List(ctx context.Context, limit int) ([]DeadTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	listed := make([]DeadTask, 0, len(d.tasks))
	for _, dead := range d.tasks {
		if limit > 0 && len(listed) >= limit {
			break
		}
		listed = append(listed, dead)
	}
	return listed, nil
}

func (d *MemoryDeadLetter) Remove(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tasks, id)
	return nil
}

func (d *MemoryDeadLetter) Len(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tasks), nil
}
