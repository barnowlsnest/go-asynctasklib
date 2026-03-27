package workerpool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type ChannelTestSuite struct {
	suite.Suite
}

func TestChannelSuite(t *testing.T) {
	suite.Run(t, new(ChannelTestSuite))
}

func (s *ChannelTestSuite) TestNewChannel() {
	tests := []struct {
		name              string
		cfg               *ChannelConfig
		expectedMaxSize   int
		expectedRetries   int
		expectedBaseDelay time.Duration
		expectedMaxDelay  time.Duration
	}{
		{
			name:              "nil_config_uses_all_defaults",
			cfg:               nil,
			expectedMaxSize:   defaultMaxSize,
			expectedRetries:   defaultMaxSubmitRetries,
			expectedBaseDelay: defaultBaseRetryDelay,
			expectedMaxDelay:  defaultMaxRetryDelay,
		},
		{
			name:              "zero_max_size_defaults",
			cfg:               &ChannelConfig{},
			expectedMaxSize:   defaultMaxSize,
			expectedRetries:   defaultMaxSubmitRetries,
			expectedBaseDelay: defaultBaseRetryDelay,
			expectedMaxDelay:  defaultMaxRetryDelay,
		},
		{
			name: "zero_delays_default",
			cfg: &ChannelConfig{
				MaxSize:          5,
				MaxSubmitRetries: 3,
			},
			expectedMaxSize:   5,
			expectedRetries:   3,
			expectedBaseDelay: defaultBaseRetryDelay,
			expectedMaxDelay:  defaultMaxRetryDelay,
		},
		{
			name: "custom_values_preserved",
			cfg: &ChannelConfig{
				MaxSize:          10,
				MaxSubmitRetries: 7,
				BaseRetryDelay:   100 * time.Millisecond,
				MaxRetryDelay:    1 * time.Second,
			},
			expectedMaxSize:   10,
			expectedRetries:   7,
			expectedBaseDelay: 100 * time.Millisecond,
			expectedMaxDelay:  1 * time.Second,
		},
		{
			name:              "negative_max_size_defaults",
			cfg:               &ChannelConfig{MaxSize: -1},
			expectedMaxSize:   defaultMaxSize,
			expectedRetries:   defaultMaxSubmitRetries,
			expectedBaseDelay: defaultBaseRetryDelay,
			expectedMaxDelay:  defaultMaxRetryDelay,
		},
	}

	for _, tc := range tests {
		tc := tc
		s.Run(tc.name, func() {
			ch := NewChannel[int](tc.cfg)
			s.Require().NotNil(ch)
			s.Equal(tc.expectedMaxSize, ch.cfg.MaxSize)
			s.Equal(tc.expectedRetries, ch.cfg.MaxSubmitRetries)
			s.Equal(tc.expectedBaseDelay, ch.cfg.BaseRetryDelay)
			s.Equal(tc.expectedMaxDelay, ch.cfg.MaxRetryDelay)
			s.Equal(tc.expectedMaxSize, cap(ch.claimsCh))
			s.NotNil(ch.activeWorkersSet)
			s.NotNil(ch.idleWorkersSet)
		})
	}
}

func (s *ChannelTestSuite) TestChannel_Subscribe() {
	s.Run("nil_worker", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 2})
		s.Require().ErrorIs(ch.Subscribe(nil), ErrInvalidWorker)
	})

	s.Run("max_pool_size_exceeded", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 1})
		w1 := s.makeWorker(1)
		w2 := s.makeWorker(2)

		s.Require().NoError(ch.Subscribe(w1))
		s.Require().ErrorIs(ch.Subscribe(w2), ErrMaxPoolSize)
	})

	s.Run("adds_to_active_set", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 5})
		w := s.makeWorker(3)

		s.Require().NoError(ch.Subscribe(w))

		ch.mu.RLock()
		_, exists := ch.activeWorkersSet[w.ID()]
		ch.mu.RUnlock()
		s.True(exists)
	})

	s.Run("removes_from_idle_set", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 5})
		w := s.makeWorker(4)
		ch.idleWorkersSet[w.ID()] = struct{}{}

		s.Require().NoError(ch.Subscribe(w))

		ch.mu.RLock()
		_, inActive := ch.activeWorkersSet[w.ID()]
		_, inIdle := ch.idleWorkersSet[w.ID()]
		ch.mu.RUnlock()
		s.True(inActive)
		s.False(inIdle)
	})
}

func (s *ChannelTestSuite) TestChannel_Unsubscribe() {
	s.Run("nil_worker_no_panic", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 2})
		s.NotPanics(func() { ch.Unsubscribe(nil) })
	})

	s.Run("moves_active_to_idle", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 5})
		w := s.makeWorker(5)

		s.Require().NoError(ch.Subscribe(w))
		ch.Unsubscribe(w)

		ch.mu.RLock()
		_, inActive := ch.activeWorkersSet[w.ID()]
		_, inIdle := ch.idleWorkersSet[w.ID()]
		ch.mu.RUnlock()
		s.False(inActive)
		s.True(inIdle)
	})
}

func (s *ChannelTestSuite) TestChannel_Claims() {
	ch := NewChannel[int](&ChannelConfig{MaxSize: 3})
	s.Equal(ch.claimsCh, ch.Claims())
}

func (s *ChannelTestSuite) TestChannel_Send() {
	s.Run("closed_claims_channel", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 1})
		close(ch.claimsCh)

		job := 42
		s.Require().ErrorIs(ch.send(&job), ErrDispatcherClosed)
	})

	s.Run("no_active_workers", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 1})
		jobCh := make(chan *int, 1)
		ch.claimsCh <- WorkClaim[int]{id: 99, jobCh: jobCh}

		job := 42
		s.Require().ErrorIs(ch.send(&job), ErrNoActiveWorkers)
	})

	s.Run("claiming_worker_not_in_active_set", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 2})
		w := s.makeWorker(1)
		s.Require().NoError(ch.Subscribe(w))

		jobCh := make(chan *int, 1)
		ch.claimsCh <- WorkClaim[int]{id: 999, jobCh: jobCh}

		job := 42
		err := ch.send(&job)
		s.Require().Error(err)
		s.Contains(err.Error(), "999")
		s.Contains(err.Error(), "not active")
	})

	s.Run("success_delivers_job", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 2})
		w := s.makeWorker(7)
		s.Require().NoError(ch.Subscribe(w))

		jobCh := make(chan *int, 1)
		ch.claimsCh <- WorkClaim[int]{id: 7, jobCh: jobCh}

		job := 42
		s.Require().NoError(ch.send(&job))

		select {
		case received := <-jobCh:
			s.Equal(42, *received)
		case <-time.After(time.Second):
			s.FailNow("job not delivered")
		}
	})
}

func (s *ChannelTestSuite) TestChannel_Submit() {
	s.Run("nil_job_immediate_error", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 1, MaxSubmitRetries: 3})
		sub := ch.Submit(context.Background(), nil)
		s.Require().NotNil(sub)

		select {
		case <-sub.Done():
		case <-time.After(time.Second):
			s.FailNow("timed out waiting for done")
		}

		s.Require().ErrorIs(sub.Err(), ErrNilJob)
	})

	s.Run("delivers_job_through_channel", func() {
		ch := NewChannel[int](&ChannelConfig{
			MaxSize:          2,
			MaxSubmitRetries: 3,
			BaseRetryDelay:   10 * time.Millisecond,
			MaxRetryDelay:    50 * time.Millisecond,
		})

		w := s.makeWorker(1)
		s.Require().NoError(ch.Subscribe(w))

		jobCh := make(chan *int, 1)
		ch.claimsCh <- WorkClaim[int]{id: 1, jobCh: jobCh}

		job := 77
		sub := ch.Submit(context.Background(), &job)
		s.Require().NotNil(sub)

		select {
		case <-sub.Done():
		case <-time.After(2 * time.Second):
			s.FailNow("timed out waiting for submit")
		}

		s.Require().NoError(sub.Err())
		s.Equal(77, *(<-jobCh))
	})

	s.Run("submit_works_without_explicit_retries_config", func() {
		ch := NewChannel[int](&ChannelConfig{MaxSize: 2})

		w := s.makeWorker(1)
		s.Require().NoError(ch.Subscribe(w))

		jobCh := make(chan *int, 1)
		ch.claimsCh <- WorkClaim[int]{id: 1, jobCh: jobCh}

		job := 33
		sub := ch.Submit(context.Background(), &job)
		s.Require().NotNil(sub)

		select {
		case <-sub.Done():
		case <-time.After(2 * time.Second):
			s.FailNow("submit failed when MaxSubmitRetries was not explicitly set")
		}

		s.Require().NoError(sub.Err())
		s.Equal(33, *(<-jobCh))
	})

	s.Run("canceled_context_completes_promptly", func() {
		ch := NewChannel[int](&ChannelConfig{
			MaxSize:          2,
			MaxSubmitRetries: 3,
			BaseRetryDelay:   10 * time.Millisecond,
			MaxRetryDelay:    50 * time.Millisecond,
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		job := 1
		sub := ch.Submit(ctx, &job)
		s.Require().NotNil(sub)

		select {
		case <-sub.Done():
		case <-time.After(2 * time.Second):
			s.FailNow("submission did not complete with canceled context")
		}
	})
}

func (s *ChannelTestSuite) TestSubmission_Cancel() {
	s.Run("nil_submission_no_panic", func() {
		var sub *Submission
		s.NotPanics(func() { sub.Cancel() })
	})

	s.Run("nil_task_no_panic", func() {
		sub := &Submission{}
		s.NotPanics(func() { sub.Cancel() })
	})
}

func (s *ChannelTestSuite) makeWorker(id uint64) *Worker[int] {
	w, err := NewWorker(context.Background(), &WorkerConfig[int]{
		ID:          id,
		HandlerFunc: noopHandlerInt,
	})
	s.Require().NoError(err)
	return w
}
