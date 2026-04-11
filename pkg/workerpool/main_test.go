package workerpool

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in this package under goleak. A goroutine that
// outlives the test that started it points to a missing Close() path, a
// leaked worker runLoop, a stuck listen()/runScaler(), or a Submit caller
// parked on a backlog send after the pool was supposed to have shut down.
//
// goleak runs after the last test in the package; if it finds a stray
// goroutine the package fails with a stack dump showing where it was
// spawned, which is enough to localize most lifecycle bugs.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
