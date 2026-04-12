package workerpool

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type (
	PoolTestSuite struct {
		suite.Suite
	}

	testJob interface {
		Sum(ctx context.Context) (int, error)
	}

	testJobImpl struct {
		arg1, arg2 int
	}
)

func (job *testJobImpl) Sum(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return job.arg1 + job.arg2, nil
}

func TestPoolSuite(t *testing.T) {
	suite.Run(t, new(PoolTestSuite))
}

func handler() HandlerFunc[testJob] {
	return func(ctx JobAware[testJob]) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		result, err := ctx.Job().Sum(ctx)
		if err != nil {
			return err
		}
		minDur := 200
		maxDur := 2900
		r := rand.New(rand.NewSource(time.Now().UnixNano()))

		<-time.After(time.Duration(r.Intn(maxDur-minDur)+minDur) * time.Millisecond)

		fmt.Println("done", result)

		return nil
	}
}

func (s *PoolTestSuite) TestPool() {
	rootCtx := context.Background()
	cfg := &Config{
		Mode:      ModeFixedSize,
		Backlog:   4000,
		RateLimit: 1000,
		ClaimsConfig: ClaimsConfig{
			Name:                 "sum",
			Size:                 runtime.NumCPU(),
			SubmitTimeout:        10 * time.Second,
			SubmitAttemptsPerSec: 5,
		},
	}
	pool, err := New[testJob](rootCtx,
		WithEvents(NewNoopEvents[testJob]()),
		WithConfig[testJob](cfg),
		WithHandler[testJob](handler()),
	)

	s.Require().NoError(err)

	ctx := s.T().Context()
	for i := range 100 {
		s.Require().NoError(pool.Submit(ctx, &testJobImpl{i + 1, 0}))
	}

	pool.GracefulShutdown()
}
