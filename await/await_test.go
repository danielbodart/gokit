package await

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestUntilReturnsOnceTheConditionHolds(t *testing.T) {
	var ready atomic.Bool
	go func() { ready.Store(true) }()
	Until(t, "the goroutine", ready.Load)
	if !ready.Load() {
		t.Error("returned before the condition held")
	}
}

func TestUntilReturnsAtOnceWhenTheConditionAlreadyHolds(t *testing.T) {
	calls := 0
	Until(t, "nothing", func() bool { calls++; return true })
	if calls != 1 {
		t.Errorf("asked %d times", calls)
	}
}

// failing stands in for a test, to see one fail.
type failing struct {
	testing.TB
}

// fatal is how failing's Fatalf ends the wait, as testing's FailNow does.
type fatal string

func (failing) Helper() {}

func (failing) Fatalf(format string, args ...any) { panic(fatal(fmt.Sprintf(format, args...))) }

func TestUntilFailsTheTestNamingWhatItWaitedFor(t *testing.T) {
	defer func() {
		if message := recover(); message != fatal("timed out waiting for the event stream to open") {
			t.Errorf("got %v", message)
		}
	}()
	until(failing{}, "the event stream to open", func() bool { return false }, 10*time.Millisecond)
}
