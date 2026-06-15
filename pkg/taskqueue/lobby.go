package taskqueue

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/yielder"
)

// Priority describes the relative scheduling importance of a Task. Higher
// values indicate greater urgency.
type Priority uint

const (
	// PriorityLow is the lowest scheduling priority.
	PriorityLow Priority = iota
	// PriorityNormal is the default scheduling priority.
	PriorityNormal
	// PriorityHigh marks tasks that should be preferred over normal ones.
	PriorityHigh
	// PrioritySuper is the highest scheduling priority.
	PrioritySuper
)

const (
	// defaultLobbyBufferSize is the buffer size used when NewLobby is given a
	// non-positive maxTasks.
	defaultLobbyBufferSize = 64
	// defaultLobbyCloseTimeout is the close timeout used when NewLobby is given
	// a non-positive closeTimeout.
	defaultLobbyCloseTimeout = 5 * time.Second
)

type (
	// Task is a unit of work managed by a Lobby. Implementations carry their own
	// identity, ordering, and scheduling metadata.
	Task interface {
		// Do executes the task, honoring ctx for cancellation.
		Do(ctx context.Context) error
		// ID returns the unique identifier of the task.
		ID() uint64
		// Seq returns the submission sequence number of the task, used to
		// preserve ordering among tasks of equal priority.
		Seq() uint64
		// Priority returns the scheduling priority of the task.
		Priority() Priority
	}

	// Lobby is a bounded, concurrency-safe staging area for tasks awaiting
	// processing. Producers enqueue work via SubmitTask or PushTasks, and
	// consumers dequeue it via RetrieveTask or FetchTasks. Once closed, a Lobby
	// rejects new submissions and exposes any remaining tasks for handoff.
	Lobby struct {
		incoming     chan Task
		closeTimeout time.Duration
		onceClose    sync.Once
		handoff      atomic.Pointer[yielder.Yielder[Task]]
		closed       atomic.Bool
	}
)

// NewLobby creates a Lobby whose incoming channel is buffered to hold up to
// maxTasks tasks and whose close operation waits up to closeTimeout for tasks to
// drain. Non-positive values fall back to defaultLobbyBufferSize and
// defaultLobbyCloseTimeout respectively.
func NewLobby(maxTasks int, closeTimeout time.Duration) *Lobby {
	if maxTasks <= 0 {
		maxTasks = defaultLobbyBufferSize
	}

	if closeTimeout <= 0 {
		closeTimeout = defaultLobbyCloseTimeout
	}

	return &Lobby{
		incoming:     make(chan Task, maxTasks),
		closeTimeout: closeTimeout,
	}
}

// SubmitTask enqueues a single task, blocking until there is room in the lobby,
// the timeout elapses, or ctx is canceled. It returns ErrLobbyClosed if the
// lobby is closed, ErrTaskNil if task is nil, ErrLobbyFull if the timeout
// elapses before space becomes available, or ctx.Err() if ctx is canceled.
func (lobby *Lobby) SubmitTask(ctx context.Context, task Task, timeout time.Duration) error {
	switch {
	case lobby.closed.Load():
		return ErrLobbyClosed
	case task == nil:
		return ErrTaskNil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return ErrLobbyFull
	case lobby.incoming <- task:
		return nil
	}
}

// PushTasks drains a yielder of tasks into the lobby once the yielder has
// finished producing. It waits for tasks to complete, propagating ctx.Err() if
// ctx is canceled or the yielder's error if it failed, then submits each
// produced task with SubmitTask using the given per-task timeout. It returns the
// first submission error encountered, or nil once all tasks are submitted.
func (lobby *Lobby) PushTasks(ctx context.Context, tasks *yielder.Yielder[Task], timeout time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tasks.Done():
			if err := tasks.Err(); err != nil {
				return err
			}
			for task := range tasks.Results() {
				if err := lobby.SubmitTask(ctx, task, timeout); err != nil {
					return err
				}
			}

			return nil
		}
	}
}

// RetrieveTask dequeues a single task, blocking until one is available, the
// timeout elapses, or ctx is canceled. It returns ErrLobbyEmpty if the timeout
// elapses before a task arrives, ErrLobbyClosed if the lobby has been closed and
// drained, or ctx.Err() if ctx is canceled.
func (lobby *Lobby) RetrieveTask(ctx context.Context, timeout time.Duration) (Task, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, ErrLobbyEmpty
	case task, ok := <-lobby.incoming:
		if !ok {
			return nil, ErrLobbyClosed
		}

		return task, nil
	}
}

// FetchTasks returns a yielder that streams up to n tasks dequeued from the
// lobby, applying the given timeout to its reads and stopping when ctx is
// canceled. It returns an error only if the yielder cannot be constructed.
func (lobby *Lobby) FetchTasks(ctx context.Context, timeout time.Duration, n int) (*yielder.Yielder[Task], error) {
	return yielder.New[Task](ctx,
		yielder.WithBuffer[Task](n),
		yielder.WithTimeout[Task](timeout),
		yielder.WithInputChannel[Task](lobby.incoming),
	)
}

// HandoffTasks returns the yielder holding any tasks left over after the lobby
// was closed. The boolean is false if the lobby is still open; when true, the
// returned yielder may be nil if CloseContext failed to build one.
func (lobby *Lobby) HandoffTasks() (*yielder.Yielder[Task], bool) {
	if !lobby.closed.Load() {
		return nil, false
	}

	return lobby.handoff.Load(), true
}

// CloseContext closes the lobby so that it no longer accepts submissions. It
// drains any buffered tasks into a handoff yielder (bounded by closeTimeout),
// which is then retrievable via HandoffTasks. CloseContext is idempotent at the
// channel level via sync.Once and returns ErrLobbyClosed if the lobby was
// already closed, or the yielder construction error if draining could not start.
func (lobby *Lobby) CloseContext(ctx context.Context) error {
	if lobby.closed.Load() {
		return ErrLobbyClosed
	}

	lobby.closed.Store(true)
	var (
		handoff *yielder.Yielder[Task]
		err     error
	)
	lobby.onceClose.Do(func() {
		defer close(lobby.incoming)
		handoff, err = yielder.New[Task](ctx,
			yielder.WithBuffer[Task](len(lobby.incoming)),
			yielder.WithTimeout[Task](lobby.closeTimeout),
			yielder.WithInputChannel[Task](lobby.incoming),
		)
		if err != nil {
			return
		}

		lobby.handoff.Store(handoff)
	})

	if err != nil {
		return err
	}

	return nil
}

// Len returns the number of tasks currently buffered in the lobby.
func (lobby *Lobby) Len() int {
	return len(lobby.incoming)
}
