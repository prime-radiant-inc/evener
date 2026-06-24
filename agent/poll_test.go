package agent

import (
	"testing"
	"time"
)

// waitForCondition polls fn until it returns true or timeout elapses, failing
// the test with desc on timeout. Use it instead of a fixed time.Sleep before
// asserting on the result of asynchronous work: it returns the instant the
// condition holds (so the happy path is fast) and tolerates scheduling delays
// when the suite runs under heavy parallelism (so the assert never fires early).
//
// Only convert POSITIVE waits ("sleep, then assert X happened") to this helper.
// A NEGATIVE wait ("sleep, then assert X has NOT happened") must keep its fixed
// sleep — there is no condition to poll for, and starvation only makes the
// absence more likely, so it does not flake.
func waitForCondition(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, desc)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
