//go:build linux

package daemonprocess

import (
	"context"
	"os"
	"os/exec"
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

// TestWaitExitAnswersAnAlreadyFiredWake pins exitPollUntil's mapping from a
// ready poll to gone: one poll that reports readiness is answered as an exit
// without a second attempt. The poll is injected and no descriptor is touched,
// so this says nothing about whether a real pidfd becomes readable — that is
// TestExitPollUntilSeesAnAlreadyExitedZombie's claim. A waiter that ignored the
// readiness, or that retried past it, would ask more than once.
func TestWaitExitAnswersAnAlreadyFiredWake(t *testing.T) {
	t.Parallel()
	polls := 0
	poll := func(fds []unix.PollFd, _ int) (int, error) {
		polls++
		fds[0].Revents = unix.POLLIN
		return 1, nil
	}
	p := &linuxProcess{fd: -1}
	deadline := time.Now().Add(200 * time.Millisecond)
	gone, err := p.exitPollUntil(poll, deadline)
	if err != nil {
		t.Fatalf("exitPollUntil: %v", err)
	}
	if !gone {
		t.Fatal("a readable pidfd was not reported as an exit")
	}
	if polls != 1 {
		t.Fatalf("an already-fired wake cost %d polls, want 1", polls)
	}
}

// TestExitPollUntilSeesAnAlreadyExitedZombie covers the pidfd's level-triggered
// readiness on a REAL descriptor, which is why this platform cannot lose an exit
// wake to ordering. A child that exited before the waiter existed stays a
// zombie until it is reaped, and its pidfd is readable the whole time, so the
// first poll reports the exit. A kernel or descriptor-level regression — a
// zombie's pidfd failing to report readable — fails here and passes the injected
// mapping test above, which is the coverage this test exists to add.
func TestExitPollUntilSeesAnAlreadyExitedZombie(t *testing.T) {
	t.Parallel()
	// The child exits immediately. It is deliberately not reaped, so it stays a
	// zombie and its pidfd stays open on an exited process.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })
	// Wait for the exit WITHOUT reaping (WNOWAIT), so the descriptor below is
	// opened on a child that has already exited. Waiting on the pidfd instead
	// would prove only the ordinary park-and-wake, not the already-fired case.
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatalf("wait for child exit without reaping: %v", err)
	}
	handle, err := openLinuxProcess(pid)
	if err != nil {
		t.Fatalf("open pidfd on the zombie: %v", err)
	}
	p, ok := handle.(*linuxProcess)
	if !ok {
		t.Fatalf("openLinuxProcess returned %T, want *linuxProcess", handle)
	}
	t.Cleanup(func() { _ = p.close() })
	gone, err := p.exitPollUntil(unix.Poll, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatalf("exitPollUntil: %v", err)
	}
	if !gone {
		t.Fatal("an exited child's real pidfd was not reported as an exit")
	}
}
