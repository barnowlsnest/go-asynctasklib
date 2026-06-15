package taskqueue

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// queueItem is a task waiting to be claimed, carrying its delivery attempt
// count so requeues preserve it.
type queueItem struct {
	task    Task
	attempt int
	index   int
}

// taskHeap orders queueItems by the backend Mode. For ModePriority, higher
// Priority comes first, ties broken FIFO by Seq; for ModeFIFO, strictly by
// Seq.
type taskHeap struct {
	items []*queueItem
	mode  Mode
}

func (h *taskHeap) Len() int { return len(h.items) }

func (h *taskHeap) Less(i, j int) bool {
	left, right := h.items[i], h.items[j]
	if h.mode == ModePriority && left.task.Priority() != right.task.Priority() {
		return left.task.Priority() > right.task.Priority()
	}
	return left.task.Seq() < right.task.Seq()
}

func (h *taskHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index = i
	h.items[j].index = j
}

func (h *taskHeap) Push(value any) {
	item := value.(*queueItem)
	item.index = len(h.items)
	h.items = append(h.items, item)
}

func (h *taskHeap) Pop() any {
	items := h.items
	last := len(items) - 1
	item := items[last]
	items[last] = nil
	item.index = -1
	h.items = items[:last]
	return item
}

// memoryLease is the in-memory Lease implementation.
type memoryLease struct {
	token    string
	task     Task
	attempt  int
	deadline time.Time
}

func (l *memoryLease) Token() string {
	return l.token
}

func (l *memoryLease) Task() Task {
	return l.task
}

func (l *memoryLease) Attempt() int {
	return l.attempt
}

func (l *memoryLease) Deadline() time.Time {
	return l.deadline
}

// leaseEntry holds a claimed item plus its visibility deadline.
type leaseEntry struct {
	item     *queueItem
	deadline time.Time
}

// MemoryBackend is the default in-memory Backend: a priority/FIFO heap of
// ready tasks plus a table of outstanding leases, guarded by a single mutex.
type MemoryBackend struct {
	mu     sync.Mutex
	ready  *taskHeap
	leases map[string]*leaseEntry
}

// NewMemoryBackend returns an in-memory backend ordering tasks by mode.
func NewMemoryBackend(mode Mode) *MemoryBackend {
	return &MemoryBackend{
		ready:  &taskHeap{mode: mode},
		leases: make(map[string]*leaseEntry),
	}
}

func (b *MemoryBackend) Enqueue(ctx context.Context, task Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task == nil {
		return ErrTaskNil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	heap.Push(b.ready, &queueItem{task: task})
	return nil
}

func (b *MemoryBackend) Claim(ctx context.Context, visibility time.Duration) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready.Len() == 0 {
		return nil, ErrQueueEmpty
	}
	item := heap.Pop(b.ready).(*queueItem)
	item.attempt++
	token := uuid.NewString()
	deadline := time.Now().Add(visibility)
	b.leases[token] = &leaseEntry{item: item, deadline: deadline}
	return &memoryLease{
		token:    token,
		task:     item.task,
		attempt:  item.attempt,
		deadline: deadline,
	}, nil
}

func (b *MemoryBackend) Ack(ctx context.Context, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.leases[token]; !exists {
		return ErrLeaseNotFound
	}
	delete(b.leases, token)
	return nil
}

func (b *MemoryBackend) Nack(ctx context.Context, token string, requeue bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, exists := b.leases[token]
	if !exists {
		return ErrLeaseNotFound
	}
	delete(b.leases, token)
	if requeue {
		heap.Push(b.ready, entry.item)
	}
	return nil
}

func (b *MemoryBackend) ReapExpired(ctx context.Context, now time.Time) ([]Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var expired []Lease
	for token, entry := range b.leases {
		if !entry.deadline.After(now) {
			expired = append(expired, &memoryLease{
				token:    token,
				task:     entry.item.task,
				attempt:  entry.item.attempt,
				deadline: entry.deadline,
			})
		}
	}
	return expired, nil
}

func (b *MemoryBackend) Len(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ready.Len(), nil
}
