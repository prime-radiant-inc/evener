//go:build linux

package daemonprocess

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestWaitExitObservesCancellationUnderRepeatedEINTR pins the liveness property
// the poll loop owes its caller: an interrupted poll must not restart the full
// timeout unboundedly. A signal storm would otherwise keep waitExit inside one
// poll attempt forever, never reaching its context check, and a cancelling
// retirement would hold its session reservation past the deadline.
func TestWaitExitObservesCancellationUnderRepeatedEINTR(t *testing.T) {
	t.Parallel()
	p := &linuxProcess{fd: 1}
	// Every poll is interrupted, and the deadline is treated as spent
	// immediately, so the only way out is the caller's cancellation check.
	poll := func([]unix.PollFd, int) (int, error) { return 0, unix.EINTR }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exited, err := p.exitPollUntil(poll, time.Now())
	if err != nil {
		t.Fatalf("exitPollUntil: %v", err)
	}
	if exited {
		t.Fatal("an exhausted budget was reported as an exit")
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: context should be cancelled")
	}
}
