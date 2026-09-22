//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The child sides of the process tests below. They are tests so the test binary
// can re-exec itself, but they do nothing unless the parent set the rendezvous
// env var. Driving mainWithDeps is the point: it exercises the production
// signal wiring rather than a hand-rolled signal.Notify.
const (
	runSIGTERMHelperEnv = "RUN_SIGTERM_GRACEFUL_CLOSE_HELPER"
	runSIGTERMReadyEnv  = "RUN_SIGTERM_GRACEFUL_CLOSE_READY"
	runSIGTERMClosedEnv = "RUN_SIGTERM_GRACEFUL_CLOSE_CLOSED"

	runSecondSignalHelperEnv = "RUN_SECOND_SIGNAL_ESCALATION_HELPER"
	runSecondSignalReadyEnv  = "RUN_SECOND_SIGNAL_ESCALATION_READY"
	runSecondSignalFirstEnv  = "RUN_SECOND_SIGNAL_ESCALATION_FIRST"

	runSignalLeakHelperEnv = "RUN_SIGNAL_LEAK_HELPER"
	runSignalLeakReadyEnv  = "RUN_SIGNAL_LEAK_READY"
)

// helperDeps builds the deps the helper children drive: production signal
// wiring with a run the parent controls through signal delivery.
func helperDeps(readyEnv string, run func(context.Context) error) mainDeps {
	deps := defaultMainDeps()
	deps.args = []string{"hello"}
	deps.stdinMode = func() (os.FileMode, error) { return 0, nil }
	deps.stdout, deps.stderr = &bytes.Buffer{}, &bytes.Buffer{}
	deps.run = func(ctx context.Context, _ runConfig) error {
		// mainWithDeps installs the handler before it calls run, so publishing
		// readiness from inside run proves it is live before the parent signals.
		// The parent must never signal into the gap.
		if err := os.WriteFile(os.Getenv(readyEnv), []byte("ready"), 0o644); err != nil {
			return err
		}
		return run(ctx)
	}
	return deps
}

func TestRunSIGTERMGracefulCloseHelperProcess(t *testing.T) {
	if os.Getenv(runSIGTERMHelperEnv) != "1" {
		t.Skip("helper process; driven by TestRunRegistersSIGTERMForGracefulClose")
	}
	mainWithDeps(helperDeps(runSIGTERMReadyEnv, func(ctx context.Context) error {
		<-ctx.Done()
		// The graceful-close half of the run path is what flushes the
		// transcript and writes --export-atif, so reaching it is the property
		// under test.
		return os.WriteFile(os.Getenv(runSIGTERMClosedEnv), []byte("closed"), 0o644)
	}))
}

func TestRunSecondSignalEscalationHelperProcess(t *testing.T) {
	if os.Getenv(runSecondSignalHelperEnv) != "1" {
		t.Skip("helper process; driven by TestRunEscalatesOnSecondSignalWhileCloseWedged")
	}
	mainWithDeps(helperDeps(runSecondSignalReadyEnv, func(ctx context.Context) error {
		// A close wedged on a provider or a lock: it never returns on its own.
		// It still watches the cancelled context only to tell the parent the
		// first delivery was processed — the runtime coalesces a same-type
		// signal that arrives while one is still pending, so the parent must
		// not send the second until the first has been handled.
		go func() {
			<-ctx.Done()
			_ = os.WriteFile(os.Getenv(runSecondSignalFirstEnv), []byte("first"), 0o644)
		}()
		select {}
	}))
}

func TestRunSignalHandlerStopsAfterRunHelperProcess(t *testing.T) {
	if os.Getenv(runSignalLeakHelperEnv) != "1" {
		t.Skip("helper process; driven by TestRunStopsSignalHandlerAfterTheRunEnds")
	}
	// A normal, clean run. mainWithDeps returns on its own and its deferred
	// cancel must have stopped the handler.
	mainWithDeps(helperDeps(runSignalLeakReadyEnv, func(context.Context) error { return nil }))
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	// Reaching here means the default disposition did not run, i.e. a signal
	// handler was still installed and swallowed the signal.
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

// waitForChildExit bounds a child's exit so a missing escalation surfaces as a
// diagnosed failure rather than an unbounded Wait.
func waitForChildExit(t *testing.T, cmd *exec.Cmd, output func() string) *exec.ExitError {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("helper exited 0, want a non-zero escalation exit\noutput:\n%s", output())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("helper exit = %v, want *exec.ExitError\noutput:\n%s", err, output())
		}
		return exitErr
	case <-time.After(15 * time.Second):
		t.Fatalf("helper never exited; the repeat signal was swallowed\noutput:\n%s", output())
		return nil
	}
}

// A campaign harness kills a run at its wall deadline with SIGTERM. The one-shot
// run path must register SIGTERM alongside SIGINT: the default disposition
// terminates the process before the deferred Session.Close runs, and that close
// is where --export-atif writes the trajectory. A timed-out run then keeps the
// record that a completed run keeps. Issue #500.
func TestRunRegistersSIGTERMForGracefulClose(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	closed := filepath.Join(dir, "closed")

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunSIGTERMGracefulCloseHelperProcess$")
	cmd.Env = append(os.Environ(),
		runSIGTERMHelperEnv+"=1",
		runSIGTERMReadyEnv+"="+ready,
		runSIGTERMClosedEnv+"="+closed,
	)
	var childOut, childErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOut, &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	output := func() string { return childOut.String() + childErr.String() }
	if _, ok := waitForFileContent(ready, 10*time.Second); !ok {
		t.Fatalf("helper never reached the run path\noutput:\n%s", output())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SIGTERM did not reach graceful close; helper exited %v\noutput:\n%s", err, output())
	}
	if _, err := os.Stat(closed); err != nil {
		t.Fatalf("graceful close never ran (ATIF export would be skipped): %v\noutput:\n%s", err, output())
	}
}

// The single channel's contract: the FIRST signal cancels without exiting, so
// the graceful close runs; a SECOND signal exits with the signal-derived code,
// so a close wedged on a provider or lock stays endable by the signal harnesses
// send rather than only by SIGKILL. One channel is what makes the count
// unambiguous — with two, whichever handler is armed first consumes the
// delivery and the other either loses the cancellation or starts its count at
// one.
func TestRunEscalatesOnSecondSignalWhileCloseWedged(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	first := filepath.Join(dir, "first")

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunSecondSignalEscalationHelperProcess$")
	cmd.Env = append(os.Environ(),
		runSecondSignalHelperEnv+"=1",
		runSecondSignalReadyEnv+"="+ready,
		runSecondSignalFirstEnv+"="+first,
	)
	var childOut, childErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOut, &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	output := func() string { return childOut.String() + childErr.String() }
	if _, ok := waitForFileContent(ready, 10*time.Second); !ok {
		t.Fatalf("helper never reached the run path\noutput:\n%s", output())
	}
	// The first delivery's graceful close is underway but wedged. Wait until the
	// child has processed it before sending the repeat, so the second delivery
	// is not coalesced into the first.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}
	if _, ok := waitForFileContent(first, 10*time.Second); !ok {
		t.Fatalf("the first SIGTERM did not cancel the run context\noutput:\n%s", output())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal repeat SIGTERM: %v", err)
	}
	exitErr := waitForChildExit(t, cmd, output)
	if code := exitErr.ExitCode(); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("escalated exit = %d, want %d\noutput:\n%s", code, 128+int(syscall.SIGTERM), output())
	}
}

// A run that ends on its own must leave no global signal handler behind: an
// in-process caller would otherwise keep swallowing SIGINT/SIGTERM. After
// mainWithDeps returns, the handler is stopped, so a signal takes the default
// disposition and terminates the child.
func TestRunStopsSignalHandlerAfterTheRunEnds(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunSignalHandlerStopsAfterRunHelperProcess$")
	cmd.Env = append(os.Environ(),
		runSignalLeakHelperEnv+"=1",
		runSignalLeakReadyEnv+"="+ready,
	)
	var childOut, childErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childOut, &childErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	output := func() string { return childOut.String() + childErr.String() }
	if _, ok := waitForFileContent(ready, 10*time.Second); !ok {
		t.Fatalf("helper never reached the run path\noutput:\n%s", output())
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatalf("handler leaked: SIGTERM after the run ended was swallowed\noutput:\n%s", output())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper exit = %v, want *exec.ExitError\noutput:\n%s", err, output())
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("helper exit = %v, want the default SIGTERM disposition\noutput:\n%s", exitErr, output())
	}
}
