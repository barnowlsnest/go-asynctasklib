package taskqueue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/yielder"
)

const (
	lobbyTimeout    = time.Second
	pollTimeout     = 20 * time.Millisecond
	fetchTimeout    = 100 * time.Millisecond
	bufferOne       = 1
	bufferRoomy     = 8
	retrievedTaskID = uint64(7)
)

type LobbySuite struct {
	suite.Suite
	ctx context.Context
}

func (s *LobbySuite) SetupTest() { s.ctx = context.Background() }

func TestLobbySuite(t *testing.T) {
	suite.Run(t, new(LobbySuite))
}

// seed places tasks directly onto the intake channel so retrieval-side tests
// are isolated from SubmitTask.
func (s *LobbySuite) seed(lobby *Lobby, tasks ...Task) {
	s.T().Helper()
	for _, task := range tasks {
		lobby.incoming <- task
	}
}

func (s *LobbySuite) TestNewLobbyAppliesDefaults() {
	lobby := NewLobby(0, 0)
	s.Require().NotNil(lobby)
	s.Zero(lobby.Len())
}

func (s *LobbySuite) TestSubmitTask() {
	cases := []struct {
		name    string
		closed  bool
		task    Task
		wantErr error
		wantLen int
	}{
		{name: "nil task returns ErrTaskNil", task: nil, wantErr: ErrTaskNil},
		{name: "closed lobby returns ErrLobbyClosed", closed: true, task: &fakeTask{id: 1, seq: 1}, wantErr: ErrLobbyClosed},
		{name: "success buffers task", task: &fakeTask{id: 1, seq: 1}, wantLen: 1},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			lobby := NewLobby(bufferRoomy, lobbyTimeout)
			if tc.closed {
				lobby.closed.Store(true)
			}

			err := lobby.SubmitTask(s.ctx, tc.task, lobbyTimeout)

			if tc.wantErr != nil {
				s.ErrorIs(err, tc.wantErr)
			} else {
				s.Require().NoError(err)
			}
			s.Equal(tc.wantLen, lobby.Len())
		})
	}
}

func (s *LobbySuite) TestSubmitTaskFullBufferTimesOut() {
	lobby := NewLobby(bufferOne, lobbyTimeout)
	s.seed(lobby, &fakeTask{id: 1, seq: 1})

	err := lobby.SubmitTask(s.ctx, &fakeTask{id: 2, seq: 2}, pollTimeout)
	s.ErrorIs(err, ErrLobbyFull)
}

func (s *LobbySuite) TestSubmitTaskHonorsCanceledContext() {
	lobby := NewLobby(bufferOne, lobbyTimeout)
	s.seed(lobby, &fakeTask{id: 1, seq: 1}) // fill the slot so the send blocks

	ctx, cancel := context.WithCancel(s.ctx)
	cancel()

	err := lobby.SubmitTask(ctx, &fakeTask{id: 2, seq: 2}, lobbyTimeout)
	s.ErrorIs(err, context.Canceled)
}

func (s *LobbySuite) TestRetrieveTask() {
	cases := []struct {
		name    string
		prepare func(lobby *Lobby) context.Context
		timeout time.Duration
		wantErr error
		wantID  uint64
	}{
		{
			name:    "empty buffer times out",
			prepare: func(*Lobby) context.Context { return s.ctx },
			timeout: pollTimeout,
			wantErr: ErrLobbyEmpty,
		},
		{
			name: "returns buffered task",
			prepare: func(lobby *Lobby) context.Context {
				s.seed(lobby, &fakeTask{id: retrievedTaskID, seq: 1})
				return s.ctx
			},
			timeout: lobbyTimeout,
			wantID:  retrievedTaskID,
		},
		{
			name: "closed lobby returns ErrLobbyClosed",
			prepare: func(lobby *Lobby) context.Context {
				s.Require().NoError(lobby.CloseContext(s.ctx))
				return s.ctx
			},
			timeout: lobbyTimeout,
			wantErr: ErrLobbyClosed,
		},
		{
			name: "canceled context returns ctx error",
			prepare: func(*Lobby) context.Context {
				ctx, cancel := context.WithCancel(s.ctx)
				cancel()
				return ctx
			},
			timeout: lobbyTimeout,
			wantErr: context.Canceled,
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			lobby := NewLobby(bufferRoomy, lobbyTimeout)
			ctx := tc.prepare(lobby)

			task, err := lobby.RetrieveTask(ctx, tc.timeout)

			if tc.wantErr != nil {
				s.ErrorIs(err, tc.wantErr)
				s.Nil(task)
			} else {
				s.Require().NoError(err)
				s.Equal(tc.wantID, task.ID())
			}
		})
	}
}

func (s *LobbySuite) TestFetchTasksYieldsBufferedTasks() {
	lobby := NewLobby(bufferRoomy, lobbyTimeout)
	tasks := []Task{&fakeTask{id: 1, seq: 1}, &fakeTask{id: 2, seq: 2}}
	s.seed(lobby, tasks...)

	stream, err := lobby.FetchTasks(s.ctx, fetchTimeout, len(tasks))
	s.Require().NoError(err)

	got := make([]uint64, 0, len(tasks))
	for task := range stream.Results() {
		got = append(got, task.ID())
	}
	// The stream drains the buffered tasks, then stops on its fetch timeout
	// because the intake channel stays open.
	s.ErrorIs(stream.Err(), yielder.ErrStopped)
	s.Len(got, len(tasks))
}

func (s *LobbySuite) TestPushTasksSubmitsEveryYieldedTask() {
	lobby := NewLobby(bufferRoomy, lobbyTimeout)
	tasks := []Task{
		&fakeTask{id: 1, seq: 1},
		&fakeTask{id: 2, seq: 2},
		&fakeTask{id: 3, seq: 3},
	}

	stream, err := yielder.New[Task](s.ctx,
		yielder.WithValues(tasks),
		yielder.WithBuffer[Task](len(tasks)),
		yielder.WithTimeout[Task](lobbyTimeout),
	)
	s.Require().NoError(err)

	s.Require().NoError(lobby.PushTasks(s.ctx, stream, lobbyTimeout))
	s.Equal(len(tasks), lobby.Len())
}

func (s *LobbySuite) TestCloseContextLifecycle() {
	lobby := NewLobby(bufferRoomy, lobbyTimeout)

	handoff, ok := lobby.HandoffTasks()
	s.False(ok)
	s.Nil(handoff)

	s.Require().NoError(lobby.CloseContext(s.ctx))
	s.ErrorIs(lobby.CloseContext(s.ctx), ErrLobbyClosed)

	handoff, ok = lobby.HandoffTasks()
	s.True(ok)
	s.NotNil(handoff)
	s.ErrorIs(lobby.SubmitTask(s.ctx, &fakeTask{id: 1, seq: 1}, lobbyTimeout), ErrLobbyClosed)
}
