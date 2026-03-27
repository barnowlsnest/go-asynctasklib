package workerpool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"golang.org/x/time/rate"
)

type PoolTestSuite struct {
	suite.Suite
}

func TestPoolSuite(t *testing.T) {
	suite.Run(t, new(PoolTestSuite))
}

func (s *PoolTestSuite) TestNew() {
	cancelledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}()

	tests := []struct {
		name    string
		ctx     context.Context
		cfg     *Config[int]
		wantErr error
	}{
		{
			name:    "nil_context",
			ctx:     nil,
			cfg:     &Config[int]{Jobs: &ChannelConfig{MaxSize: 1}},
			wantErr: ErrNilCfg,
		},
		{
			name:    "canceled_context",
			ctx:     cancelledCtx,
			cfg:     &Config[int]{Jobs: &ChannelConfig{MaxSize: 1}},
			wantErr: context.Canceled,
		},
		{
			name:    "nil_config",
			ctx:     context.Background(),
			cfg:     nil,
			wantErr: ErrNilCfg,
		},
		{
			name:    "nil_jobs_config",
			ctx:     context.Background(),
			cfg:     &Config[int]{},
			wantErr: ErrNilCfg,
		},
	}

	for _, tc := range tests {
		tc := tc
		s.Run(tc.name, func() {
			wp, err := New[int](tc.ctx, tc.cfg)
			s.Nil(wp)
			s.Require().ErrorIs(err, tc.wantErr)
		})
	}

	s.Run("valid_config", func() {
		cfg := &Config[int]{
			Jobs:    &ChannelConfig{MaxSize: 5},
			Handler: noopHandlerInt,
		}
		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)
		s.Require().NotNil(wp)
		s.Equal(5, cap(wp.workers))
		s.NotNil(wp.jobs)
		s.NotNil(wp.cfg)
	})

	s.Run("zero_max_size_uses_default_capacity", func() {
		cfg := &Config[int]{
			Jobs:    &ChannelConfig{MaxSize: 0},
			Handler: noopHandlerInt,
		}
		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)
		s.Equal(defaultMaxSize, cap(wp.workers))
	})

	s.Run("nil_on_failed_submit_gets_default", func() {
		cfg := &Config[int]{
			Jobs: &ChannelConfig{MaxSize: 2},
		}
		s.Nil(cfg.OnFailedSubmit)

		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)
		s.Require().NotNil(wp)
		s.NotNil(cfg.OnFailedSubmit)
	})

	s.Run("nil_rate_limit_gets_default", func() {
		cfg := &Config[int]{
			Jobs: &ChannelConfig{MaxSize: 2},
		}
		s.Nil(cfg.RateLimit)

		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)
		s.Require().NotNil(wp)
		s.NotNil(cfg.RateLimit)
	})

	s.Run("custom_rate_limit_preserved", func() {
		rl := rate.NewLimiter(rate.Every(time.Second), 10)
		cfg := &Config[int]{
			Jobs:      &ChannelConfig{MaxSize: 2},
			RateLimit: rl,
		}
		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)
		s.Equal(rl, wp.cfg.RateLimit)
	})
}

func (s *PoolTestSuite) TestSubmit() {
	s.Run("nil_context_returns_error", func() {
		wp := s.makePool()
		job := 1
		s.Require().ErrorIs(wp.Submit(nil, &job), ErrNilCtx) //nolint:staticcheck // intentionally testing nil context
	})

	s.Run("canceled_context", func() {
		wp := s.makePool()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		job := 1
		s.Require().ErrorIs(wp.Submit(ctx, &job), context.Canceled)
	})

	s.Run("nil_job", func() {
		wp := s.makePool()
		s.Require().ErrorIs(wp.Submit(context.Background(), nil), ErrNilJob)
	})

	s.Run("delivers_job_to_worker", func() {
		wp := s.makePool()

		w := s.makeWorker(1)
		s.Require().NoError(wp.jobs.Subscribe(w))

		jobCh := make(chan *int, 1)
		wp.jobs.claimsCh <- WorkClaim[int]{id: 1, jobCh: jobCh}

		job := 55
		err := wp.Submit(context.Background(), &job)
		s.Require().NoError(err)
		s.Equal(55, *(<-jobCh))
	})

	s.Run("rate_limiter_rejects_when_burst_zero", func() {
		cfg := &Config[int]{
			Jobs:      &ChannelConfig{MaxSize: 2, MaxSubmitRetries: 3},
			RateLimit: rate.NewLimiter(rate.Every(time.Hour), 0),
		}
		wp, err := New[int](context.Background(), cfg)
		s.Require().NoError(err)

		job := 1
		err = wp.Submit(context.Background(), &job)
		s.Require().Error(err)
	})
}

func (s *PoolTestSuite) makePool() *WorkerPool[int] {
	cfg := &Config[int]{
		Jobs: &ChannelConfig{
			MaxSize:          2,
			MaxSubmitRetries: 3,
			BaseRetryDelay:   100 * time.Millisecond,
			MaxRetryDelay:    time.Second,
		},
	}
	wp, err := New[int](context.Background(), cfg)
	s.Require().NoError(err)
	return wp
}

func (s *PoolTestSuite) makeWorker(id uint64) *Worker[int] {
	w, err := NewWorker(context.Background(), &WorkerConfig[int]{
		ID:          id,
		HandlerFunc: noopHandlerInt,
	})
	s.Require().NoError(err)
	return w
}
