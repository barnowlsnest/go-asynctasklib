package taskqueue

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopQueueEvents_SatisfiesInterface(t *testing.T) {
	var events QueueEvents = NewNoopQueueEvents()
	require.NotNil(t, events)

	assert.NotPanics(t, func() {
		events.OnEnqueue(nil)
		events.OnClaim(nil)
		events.OnAck(nil)
		events.OnNack(nil, nil)
		events.OnRetry(nil, 0)
		events.OnDeadLetter(nil, nil)
		events.OnLeaseExpired(nil)
	})
}
