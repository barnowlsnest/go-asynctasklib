package taskqueue_test

import (
	"context"
	"fmt"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/taskqueue"
)

// printTask is a minimal taskqueue.Task: it prints its label when run.
type printTask struct {
	id       uint64
	seq      uint64
	priority taskqueue.Priority
	label    string
}

func (t printTask) Do(context.Context) error {
	fmt.Println("processing", t.label)
	return nil
}

func (t printTask) ID() uint64                   { return t.id }
func (t printTask) Seq() uint64                  { return t.seq }
func (t printTask) Priority() taskqueue.Priority { return t.priority }

// Example_basicUsage enqueues three tasks at different priorities into a queue
// backed by the default in-memory backend, then claims, runs, and acks each
// one. Higher-priority tasks are delivered first, so the output is ordered by
// priority regardless of enqueue order.
func Example_basicUsage() {
	ctx := context.Background()

	queue, err := taskqueue.New(ctx,
		taskqueue.WithMode(taskqueue.ModePriority),
		taskqueue.WithMaxAttempts(3),
	)
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	defer func() {
		if err := queue.Close(ctx); err != nil {
			fmt.Println("close:", err)
		}
	}()

	tasks := []printTask{
		{id: 1, seq: 1, priority: taskqueue.PriorityLow, label: "low"},
		{id: 2, seq: 2, priority: taskqueue.PrioritySuper, label: "super"},
		{id: 3, seq: 3, priority: taskqueue.PriorityHigh, label: "high"},
	}
	for _, task := range tasks {
		if err := queue.Enqueue(ctx, task); err != nil {
			fmt.Println("enqueue:", err)
			return
		}
	}

	const claimTimeout = time.Second
	for range tasks {
		claim, err := queue.Dequeue(ctx, claimTimeout)
		if err != nil {
			fmt.Println("dequeue:", err)
			return
		}
		if runErr := claim.Task().Do(ctx); runErr != nil {
			if nackErr := claim.Nack(ctx, runErr); nackErr != nil {
				fmt.Println("nack:", nackErr)
			}
			continue
		}
		if err := claim.Ack(ctx); err != nil {
			fmt.Println("ack:", err)
			return
		}
	}

	// Output:
	// processing super
	// processing high
	// processing low
}
