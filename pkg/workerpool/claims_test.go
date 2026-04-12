package workerpool

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type (
	ClaimsTestSuite struct {
		suite.Suite
	}

	mockClaimsWorker struct {
		id uint64
	}
)

func (m *mockClaimsWorker) ID() uint64 { return m.id }

func TestClaimsSuite(t *testing.T) {
	suite.Run(t, new(ClaimsTestSuite))
}

func (s *ClaimsTestSuite) TestNewClaims() {
	testCases := []struct {
		title       string
		cfg         *ClaimsConfig
		expectedErr error
	}{
		{
			title:       "nil config",
			cfg:         nil,
			expectedErr: ErrNil,
		},
		{
			title: "empty config gets defaults",
			cfg:   &ClaimsConfig{},
		},
		{
			title: "explicit config",
			cfg: &ClaimsConfig{
				Name:          "test",
				Size:          4,
				SubmitTimeout: time.Second,
				SubmitBackoff: 10 * time.Millisecond,
			},
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			c, err := newClaims[int](tc.cfg)
			if tc.expectedErr != nil {
				s.Require().ErrorIs(err, tc.expectedErr)
				s.Require().Nil(c)
				return
			}
			s.Require().NoError(err)
			s.Require().NotNil(c)
			s.Require().NotNil(c.claimsCh)
			s.Require().NotNil(c.subscribers)
		})
	}
}

func (s *ClaimsTestSuite) TestApplyDefaults() {
	testCases := []*struct {
		title    string
		input    ClaimsConfig
		expected ClaimsConfig
	}{
		{
			title: "all zero gets defaults",
			input: ClaimsConfig{},
			expected: ClaimsConfig{
				Size:                 runtime.NumCPU(),
				SubmitBackoff:        defaultSubmitBackoff,
				SubmitTimeout:        defaultSubmitTimeout,
				SubmitAttemptsPerSec: defaultSubmitAttemptsPerSec,
				BackoffFactor:        defaultBackoffFactor,
			},
		},
		{
			title: "explicit values preserved",
			input: ClaimsConfig{
				Name:                 "named",
				Size:                 3,
				SubmitBackoff:        20 * time.Millisecond,
				SubmitTimeout:        500 * time.Millisecond,
				SubmitAttemptsPerSec: 8,
				BackoffFactor:        0.25,
			},
			expected: ClaimsConfig{
				Name:                 "named",
				Size:                 3,
				SubmitBackoff:        20 * time.Millisecond,
				SubmitTimeout:        500 * time.Millisecond,
				SubmitAttemptsPerSec: 8,
				BackoffFactor:        0.25,
			},
		},
		{
			title: "backoff factor > 1 resets to default",
			input: ClaimsConfig{
				Size:          2,
				BackoffFactor: 2.5,
			},
			expected: ClaimsConfig{
				Size:                 2,
				SubmitBackoff:        defaultSubmitBackoff,
				SubmitTimeout:        defaultSubmitTimeout,
				SubmitAttemptsPerSec: defaultSubmitAttemptsPerSec,
				BackoffFactor:        defaultBackoffFactor,
			},
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			got := tc.input
			got.applyDefaults()
			s.Require().Equal(tc.expected, got)
		})
	}
}

func (s *ClaimsTestSuite) TestSubscribe() {
	testCases := []struct {
		title        string
		size         int
		presubscribe []uint64
		worker       Subscriber[int]
		expectedErr  error
	}{
		{
			title:       "nil worker",
			size:        2,
			worker:      nil,
			expectedErr: ErrInvalidWorker,
		},
		{
			title:  "first subscribe",
			size:   2,
			worker: &mockClaimsWorker{id: 1},
		},
		{
			title:        "overwrites existing id",
			size:         2,
			presubscribe: []uint64{1},
			worker:       &mockClaimsWorker{id: 1},
		},
		{
			title:        "exceeds capacity",
			size:         2,
			presubscribe: []uint64{10, 20},
			worker:       &mockClaimsWorker{id: 30},
			expectedErr:  ErrMaxPoolSize,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			c, err := newClaims[int](&ClaimsConfig{Size: tc.size})
			s.Require().NoError(err)

			for _, id := range tc.presubscribe {
				s.Require().NoError(c.Subscribe(&mockClaimsWorker{id: id}))
			}

			err = c.Subscribe(tc.worker)
			if tc.expectedErr != nil {
				s.Require().ErrorIs(err, tc.expectedErr)
				return
			}
			s.Require().NoError(err)
			s.Require().Contains(c.subscribers, tc.worker.ID())
		})
	}
}

func (s *ClaimsTestSuite) TestUnsubscribe() {
	testCases := []struct {
		title        string
		presubscribe []uint64
		worker       Subscriber[int]
		wantRemoved  bool
	}{
		{
			title:  "nil worker is noop",
			worker: nil,
		},
		{
			title:  "non-existent worker is noop",
			worker: &mockClaimsWorker{id: 99},
		},
		{
			title:        "existing worker removed",
			presubscribe: []uint64{7},
			worker:       &mockClaimsWorker{id: 7},
			wantRemoved:  true,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.title, func() {
			c, err := newClaims[int](&ClaimsConfig{Size: 4})
			s.Require().NoError(err)

			for _, id := range tc.presubscribe {
				s.Require().NoError(c.Subscribe(&mockClaimsWorker{id: id}))
			}

			s.Require().NoError(c.Unsubscribe(tc.worker))

			if tc.wantRemoved {
				s.Require().NotContains(c.subscribers, tc.worker.ID())
			}
		})
	}
}

func (s *ClaimsTestSuite) TestSubmit_ContextCanceled() {
	c, err := newClaims[int](&ClaimsConfig{Size: 1, SubmitTimeout: time.Second})
	s.Require().NoError(err)

	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	job := newJobContext(ctx, ctx, new(1))
	s.Require().ErrorIs(c.submit(job), context.Canceled)
}

func (s *ClaimsTestSuite) TestSubmit_NoWorkers() {
	c, err := newClaims[int](&ClaimsConfig{
		Size:          1,
		SubmitTimeout: 80 * time.Millisecond,
	})
	s.Require().NoError(err)

	ctx := s.T().Context()
	s.Require().ErrorIs(c.submit(newJobContext(ctx, ctx, new(1))), ErrNoWorkers)
}

func (s *ClaimsTestSuite) TestSubmit_HappyPath() {
	c, err := newClaims[int](&ClaimsConfig{
		Size:          1,
		SubmitTimeout: time.Second,
	})
	s.Require().NoError(err)

	worker := &mockClaimsWorker{id: 1}
	s.Require().NoError(c.Subscribe(worker))

	input := make(chan JobAware[int], 1)
	c.claimsCh <- &claim[int]{id: worker.id, input: input}

	ctx := s.T().Context()
	job := newJobContext(ctx, ctx, new(42))
	s.Require().NoError(c.submit(job))

	select {
	case got := <-input:
		s.Require().NotNil(got)
		s.Require().Equal(42, *got.Job())
	case <-time.After(time.Second):
		s.FailNow("job was not delivered")
	}
}

func (s *ClaimsTestSuite) TestSubmit_DispatcherClosed() {
	c, err := newClaims[int](&ClaimsConfig{
		Size:          1,
		SubmitTimeout: time.Second,
	})
	s.Require().NoError(err)

	close(c.claimsCh)

	ctx := s.T().Context()
	job := newJobContext(ctx, ctx, new(1))
	s.Require().ErrorIs(c.submit(job), ErrDispatcherClosed)
}

// TestSubmit_StaleClaimTimesOut exercises the path where a claim is pulled
// from the queue but the advertising worker is no longer reading its input
// channel: Submit should backoff, retry, and eventually return
// ErrSubmitTimeout (not ErrNoWorkers, since the worker is still subscribed).
func (s *ClaimsTestSuite) TestSubmit_StaleClaimTimesOut() {
	c, err := newClaims[int](&ClaimsConfig{
		Size:          1,
		SubmitTimeout: 120 * time.Millisecond,
		SubmitBackoff: 10 * time.Millisecond,
	})
	s.Require().NoError(err)

	worker := &mockClaimsWorker{id: 1}
	s.Require().NoError(c.Subscribe(worker))

	staleInput := make(chan JobAware[int]) // no reader
	c.claimsCh <- &claim[int]{id: worker.id, input: staleInput}

	ctx := s.T().Context()
	job := newJobContext(ctx, ctx, new(1))
	s.Require().ErrorIs(c.submit(job), ErrSubmitTimeout)
}

func (s *ClaimsTestSuite) TestMetadataGetters() {
	c, err := newClaims[int](&ClaimsConfig{Name: "pipeline", Size: 3})
	s.Require().NoError(err)

	s.Require().Equal("pipeline", c.Name())
	s.Require().Equal(3, c.Size())
	s.Require().Equal(0, c.PendingClaims())
	s.Require().NotNil(c.Claims())

	input := make(chan JobAware[int])
	c.claimsCh <- &claim[int]{id: 1, input: input}
	s.Require().Equal(1, c.PendingClaims())
}
