package taskqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
)

var errDead = errors.New("boom")

type MemoryDeadLetterSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *MemoryDeadLetterSuite) SetupTest() { s.ctx = context.Background() }

func TestMemoryDeadLetterSuite(t *testing.T) {
	suite.Run(t, new(MemoryDeadLetterSuite))
}

func (s *MemoryDeadLetterSuite) TestAddListRemoveLen() {
	const taskID = uint64(7)
	dlq := NewMemoryDeadLetter()

	s.Require().NoError(dlq.Add(s.ctx, &fakeTask{id: taskID, seq: 1}, errDead))

	length, err := dlq.Len(s.ctx)
	s.Require().NoError(err)
	s.Equal(1, length)

	listed, err := dlq.List(s.ctx, 10)
	s.Require().NoError(err)
	s.Require().Len(listed, 1)
	s.Equal(taskID, listed[0].Task.ID())
	s.ErrorIs(listed[0].Reason, errDead)

	s.Require().NoError(dlq.Remove(s.ctx, taskID))
	length, err = dlq.Len(s.ctx)
	s.Require().NoError(err)
	s.Zero(length)
}

func (s *MemoryDeadLetterSuite) TestListRespectsLimit() {
	dlq := NewMemoryDeadLetter()
	const total = 5
	for id := uint64(1); id <= total; id++ {
		s.Require().NoError(dlq.Add(s.ctx, &fakeTask{id: id, seq: id}, errDead))
	}

	const limit = 2
	listed, err := dlq.List(s.ctx, limit)
	s.Require().NoError(err)
	s.Len(listed, limit)
}

func (s *MemoryDeadLetterSuite) TestAddNilTask() {
	dlq := NewMemoryDeadLetter()
	s.ErrorIs(dlq.Add(s.ctx, nil, errDead), ErrTaskNil)
}
