//go:build linux || darwin

package execenv

import (
	"bytes"
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

// TestW2Conc_StreamCommandSignalEscalatesToSIGKILL pins the SIGTERM->SIGKILL
// escalation goroutine inside StreamCommand. The shell traps and ignores
// SIGTERM, so it can only be reaped by the SIGKILL escalation that fires after
// terminateGrace (shrunk to 200ms for tests). Wait returning therefore proves
// the escalation arm (the group-wide SIGKILL after timer.C) executed.
func TestW2Conc_StreamCommandSignalEscalatesToSIGKILL(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	// The bash loop ignores SIGTERM and only ever spawns short-lived sleep
	// children, so a group SIGTERM cannot reap the loop itself: bash survives
	// (trap ignores TERM) and immediately respawns. Only the un-trappable
	// SIGKILL escalation after terminateGrace can end it. READY is printed only
	// after the trap is installed, so waiting for it avoids racing a SIGTERM in
	// before the handler is armed.
	out := newReadyWriter("READY")
	h, err := env.StreamCommand(context.Background(),
		"trap '' TERM; echo READY; while :; do sleep 0.05; done", "", nil,
		out)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	done := make(chan int, 1)
	go func() { c, _ := h.Wait(); done <- c }()

	select {
	case <-out.ready:
	case code := <-done:
		t.Fatalf("process exited (code %d) before READY reached the streamed output: %q", code, out.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("READY did not reach the streamed output within 5s: %q", out.String())
	}

	// SIGTERM is trapped, so this alone would never stop the loop; only the
	// escalation to SIGKILL after terminateGrace reaps it.
	start := time.Now()
	h.Signal()

	select {
	case <-done:
		// Process reaped -> SIGKILL escalation arm ran.
	case <-time.After(5 * time.Second):
		t.Fatal("Signal did not escalate to SIGKILL within 5s")
	}
	if elapsed := time.Since(start); elapsed < terminateGrace {
		t.Fatalf("process died in %s, before the %s grace: SIGKILL escalation was not exercised",
			elapsed, terminateGrace)
	}

	if !w2conc_pidGone(h.Pid) {
		t.Fatalf("pid %d still alive after SIGKILL escalation", h.Pid)
	}
}

// TestW2Conc_StreamCommandCtxCancelSignals pins the ctx-watcher goroutine arm:
// cancelling the context must fire signal(), terminating the process group even
// though the caller never calls Signal itself.
func TestW2Conc_StreamCommandCtxCancelSignals(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h, err := env.StreamCommand(ctx, "sleep 30", "", nil, &bytes.Buffer{})
	if err != nil {
		cancel()
		t.Fatalf("stream: %v", err)
	}

	done := make(chan int, 1)
	go func() { c, _ := h.Wait(); done <- c }()

	cancel()

	select {
	case <-done:
		// ctx.Done arm fired signal() and the process exited.
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel did not terminate the process within 5s")
	}
}

// TestW2Conc_StreamCommandCtxAlreadyDone pins the pre-start ctx-done select arm:
// a context cancelled before StreamCommand returns must yield ctx.Err() with no
// process launched.
func TestW2Conc_StreamCommandCtxAlreadyDone(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h, err := env.StreamCommand(ctx, "sleep 30", "", nil, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected ctx error, got handle %+v", h)
	}
	if !errorsIsCanceled(err) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func errorsIsCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// w2conc_pidGone reports whether pid no longer exists.
func w2conc_pidGone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}
