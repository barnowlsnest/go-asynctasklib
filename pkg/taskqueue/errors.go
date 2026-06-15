package taskqueue

import (
	"errors"
)

var (
	ErrLobbyFull   = errors.New("could not submit to lobby within timeout")
	ErrLobbyEmpty  = errors.New("could not retrieve task from lobby within timeout")
	ErrLobbyClosed = errors.New("lobby already has been closed")
)

var (
	ErrTaskNil = errors.New("task cannot be nil")
)

var (
	ErrQueueEmpty    = errors.New("no task available to claim")
	ErrQueueClosed   = errors.New("queue already has been closed")
	ErrLeaseNotFound = errors.New("lease token not found")
	ErrClaimSettled  = errors.New("claim already acked or nacked")
	ErrInvalidQueue  = errors.New("invalid queue configuration")
)

var (
	ErrLeaseExpired = errors.New("lease expired before ack")
)
