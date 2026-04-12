package workerpool_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/barnowlsnest/go-asynctasklib/v2/pkg/workerpool"
)

// Example_basicUsage shows how to spin up a fixed-size pool of workers, feed
// it a handful of jobs, and shut it down gracefully so every submitted job
// runs to completion. One worker is used so the output is deterministic.
func Example_basicUsage() {
	var (
		mu      sync.Mutex
		results []int
	)

	handler := func(ctx workerpool.JobAware[int]) error {
		mu.Lock()
		results = append(results, ctx.Job()*2)
		mu.Unlock()
		return nil
	}

	pool, err := workerpool.New[int](context.Background(),
		workerpool.WithConfig[int](&workerpool.Config{
			Mode:      workerpool.ModeFixedSize,
			Backlog:   8,
			RateLimit: 1000,
			ClaimsConfig: workerpool.ClaimsConfig{
				Size:          1,
				SubmitTimeout: time.Second,
			},
		}),
		workerpool.WithHandler[int](handler),
	)
	if err != nil {
		fmt.Println("new:", err)
		return
	}

	const jobs = 3
	for i := range jobs {
		if err := pool.Submit(context.Background(), i+1); err != nil {
			fmt.Println("submit:", err)
			return
		}
	}

	pool.GracefulShutdown()

	mu.Lock()
	defer mu.Unlock()
	for _, r := range results {
		fmt.Println(r)
	}
	// Output:
	// 2
	// 4
	// 6
}
