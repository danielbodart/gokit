package await

import (
	"testing"
	"time"
)

// Timeout is how long Until waits before failing the test: long enough for a
// loaded CI runner or a race-detector build to get round to anything a test
// waits on, and short enough that a condition that will never hold fails the
// test rather than hanging it.
const Timeout = 5 * time.Second

// Until polls done until it holds, failing the test with "timed out waiting
// for <what>" if it has not after Timeout.
//
// What a wait is for reads as the end of "waiting for", such as "the event
// stream to open".
func Until(t testing.TB, what string, done func() bool) {
	t.Helper()
	until(t, what, done, Timeout)
}

// until is Until with its timeout given, so its own test need not sit
// through Timeout to see it fail.
//
// The poll's millisecond sleep is the one real wait a test makes, and it
// measures no time: it only hands the processor to the goroutines being
// waited on.
func until(t testing.TB, what string, done func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
			return
		}
		time.Sleep(time.Millisecond)
	}
}
