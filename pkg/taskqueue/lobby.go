package taskqueue

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/yielder"
)

type Priority uint

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
	PrioritySuper
)

const (
	defaultLobbyBufferSize   = 64
	defaultLobbyCloseTimeout = 5 * time.Second
)

type (
	Task interface {
		Do(ctx context.Context) error
		ID() uint64
		Seq() uint64
		Priority() Priority
	}

	Lobby struct {
		incoming     chan Task
		closeTimeout time.Duration
		onceClose    sync.Once
		handoff      atomic.Pointer[yielder.Yielder[Task]]
		closed       atomic.Bool
	}
)

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

func (lobby *Lobby) FetchTasks(ctx context.Context, timeout time.Duration, n int) (*yielder.Yielder[Task], error) {
	return yielder.New[Task](ctx,
		yielder.WithBuffer[Task](n),
		yielder.WithTimeout[Task](timeout),
		yielder.WithInputChannel[Task](lobby.incoming),
	)
}

func (lobby *Lobby) HandoffTasks() (*yielder.Yielder[Task], bool) {
	if !lobby.closed.Load() {
		return nil, false
	}

	return lobby.handoff.Load(), true
}

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

func (lobby *Lobby) Len() int {
	return len(lobby.incoming)
}
