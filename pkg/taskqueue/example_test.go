package taskqueue_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/taskqueue"
)

const exampleClaimTimeout = time.Second

// loggingTask is a minimal taskqueue.Task that logs its label through an
// injected logger when run, so examples emit their output via s.T().Logf
// rather than the fmt package.
type loggingTask struct {
	id       uint64
	seq      uint64
	priority taskqueue.Priority
	label    string
	logf     func(format string, args ...any)
}

func (t loggingTask) Do(context.Context) error {
	t.logf("processing %s", t.label)
	return nil
}

func (t loggingTask) ID() uint64                   { return t.id }
func (t loggingTask) Seq() uint64                  { return t.seq }
func (t loggingTask) Priority() taskqueue.Priority { return t.priority }

type ExampleSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *ExampleSuite) SetupTest() { s.ctx = context.Background() }

func TestExampleSuite(t *testing.T) {
	suite.Run(t, new(ExampleSuite))
}

// runAll claims, runs, and acks every task, returning the order in which task
// IDs were delivered.
func (s *ExampleSuite) runAll(queue *taskqueue.Queue, count int) []uint64 {
	s.T().Helper()
	delivered := make([]uint64, 0, count)
	for range count {
		claim, err := queue.Dequeue(s.ctx, exampleClaimTimeout)
		s.Require().NoError(err)
		s.Require().NoError(claim.Task().Do(s.ctx))
		delivered = append(delivered, claim.Task().ID())
		s.Require().NoError(claim.Ack(s.ctx))
	}
	return delivered
}

// TestDeliveryOrderByMode shows the same enqueue/claim/run/ack flow against a
// queue backed by the default in-memory backend, under both ordering modes.
// ModePriority delivers the highest priority first; ModeFIFO ignores priority
// and delivers in Seq order.
func (s *ExampleSuite) TestDeliveryOrderByMode() {
	cases := []struct {
		name  string
		mode  taskqueue.Mode
		tasks []loggingTask
		want  []uint64
	}{
		{
			name: "priority mode delivers highest priority first",
			mode: taskqueue.ModePriority,
			tasks: []loggingTask{
				{id: 1, seq: 1, priority: taskqueue.PriorityLow, label: "low"},
				{id: 2, seq: 2, priority: taskqueue.PrioritySuper, label: "super"},
				{id: 3, seq: 3, priority: taskqueue.PriorityHigh, label: "high"},
			},
			want: []uint64{2, 3, 1},
		},
		{
			name: "fifo mode ignores priority and delivers by seq",
			mode: taskqueue.ModeFIFO,
			tasks: []loggingTask{
				{id: 1, seq: 1, priority: taskqueue.PrioritySuper, label: "first"},
				{id: 2, seq: 2, priority: taskqueue.PriorityLow, label: "second"},
				{id: 3, seq: 3, priority: taskqueue.PriorityHigh, label: "third"},
			},
			want: []uint64{1, 2, 3},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			queue, err := taskqueue.New(s.ctx, taskqueue.WithMode(tc.mode))
			s.Require().NoError(err)
			defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

			for _, task := range tc.tasks {
				task.logf = s.T().Logf
				s.Require().NoError(queue.Enqueue(s.ctx, task))
			}

			delivered := s.runAll(queue, len(tc.tasks))
			s.T().Logf("%s: delivery order %v", tc.name, delivered)
			s.Equal(tc.want, delivered)
		})
	}
}

// TestLobbyFeedsPriorityQueue shows the intended pipeline: a Lobby is the FIFO
// intake front door, and a priority-mode Queue is the ordered storage behind
// it. Tasks are submitted to the lobby in arbitrary order, drained into the
// queue, then delivered highest priority first.
func (s *ExampleSuite) TestLobbyFeedsPriorityQueue() {
	lobby := taskqueue.NewLobby(0, exampleClaimTimeout)

	queue, err := taskqueue.New(s.ctx, taskqueue.WithMode(taskqueue.ModePriority))
	s.Require().NoError(err)
	defer func() { s.Require().NoError(queue.Close(s.ctx)) }()

	// The producer submits in arbitrary order into the intake lobby.
	submitted := []loggingTask{
		{id: 1, seq: 1, priority: taskqueue.PriorityLow, label: "low", logf: s.T().Logf},
		{id: 2, seq: 2, priority: taskqueue.PrioritySuper, label: "super", logf: s.T().Logf},
		{id: 3, seq: 3, priority: taskqueue.PriorityHigh, label: "high", logf: s.T().Logf},
	}
	for _, task := range submitted {
		s.Require().NoError(lobby.SubmitTask(s.ctx, task, exampleClaimTimeout))
	}

	// Drain the lobby into the priority queue. Intake order is FIFO; the queue
	// reorders by priority on the way out.
	for range submitted {
		task, err := lobby.RetrieveTask(s.ctx, exampleClaimTimeout)
		s.Require().NoError(err)
		s.Require().NoError(queue.Enqueue(s.ctx, task))
	}

	delivered := s.runAll(queue, len(submitted))
	s.T().Logf("lobby -> priority queue delivery order: %v", delivered)
	s.Equal([]uint64{2, 3, 1}, delivered)
}
