package main

import (
	"context"
	"testing"
	"time"
)

// TestRunAskResponderCommandBoundsBackgroundedChild: a responder that
// backgrounds a long-running child (or a pipeline stage that outlives it)
// must not hold runAskResponderCommand past its timeout plus WaitDelay.
// exec.CommandContext's default single-process kill reaches only the direct
// `sh`; a grandchild it left behind can keep the captured stdout pipe open
// indefinitely, wedging the whole `evener run` on a real question long after
// the deadline passed (agent/internal/hooks/command_runtime.go faces and
// fixes the exact same shell-command problem via procgroup + WaitDelay).
//
// The call itself runs in a goroutine with its own bound, so a regression
// here fails this test in ~5s rather than hanging the suite for the
// backgrounded sleep's full duration.
func TestRunAskResponderCommandBoundsBackgroundedChild(t *testing.T) {
	responder := "sleep 5 & echo done"

	const timeout = 200 * time.Millisecond
	const bound = 5 * time.Second // timeout + askResponderWaitDelay + generous slack

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_, _ = runAskResponderCommand(context.Background(), responder, nil, timeout)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > bound {
			t.Fatalf("runAskResponderCommand took %s, want it bounded by the timeout (%s) plus WaitDelay, not the backgrounded sleep", elapsed, timeout)
		}
	case <-time.After(bound):
		t.Fatalf("runAskResponderCommand did not return within %s; a backgrounded child outlived the timeout", bound)
	}
}
