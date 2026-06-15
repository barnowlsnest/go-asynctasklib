package taskqueue

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in this package under goleak. A goroutine that
// outlives the test that started it points to a missing Close() path or a
// leaked reaper loop.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
