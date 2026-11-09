//go:build unix

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

type probeRun struct {
	out []byte
	err error
}

const loginShellPathFixtureTripwire = 30 * time.Second

var (
	errProbeFinishedBeforeFixture = errors.New("probe finished before the fixture established its orphan pipe")
	errFixtureStartupTimedOut     = errors.New("fixture startup timed out")
)

// waitForProbeFixture waits for the orphan-pipe fixture and the probe at the
// same time. A shell that fails to start (a fork failure under load, for
// example) leaves h.AwaitStarted blocking forever; cleanup cannot release the
// FIFO until the test returns, so the package would hang to its global timeout.
// Observing probe completion and a bounded tripwire terminates the wait.
func waitForProbeFixture(h *orphanpipetest.Holder, done <-chan probeRun, tripwire time.Duration) error {
	started := make(chan struct{})
	go func() {
		h.AwaitStarted()
		close(started)
	}()
	trip := time.NewTimer(tripwire)
	defer trip.Stop()
	select {
	case <-started:
		return nil
	case got := <-done:
		// The probe may have finished just after the fixture established itself;
		// a ready started signal wins over declaring a startup failure.
		select {
		case <-started:
			return nil
		default:
		}
		return fmt.Errorf("%w (output %q)", errProbeFinishedBeforeFixture, got.out)
	case <-trip.C:
		return errFixtureStartupTimedOut
	}
}

func awaitFixtureStarted(t *testing.T, h *orphanpipetest.Holder, done <-chan probeRun) {
	t.Helper()
	if err := waitForProbeFixture(h, done, loginShellPathFixtureTripwire); err != nil {
		t.Fatal(err)
	}
}

// The login-shell probe's only bound is loginShellPATHTimeout, and it runs the
// user's rc chain, which may background anything (an agent, a daemon, a
// `something &` line). That job inherits the probe's stdout, and killing the
// shell when the timeout ends does not kill it, so without a WaitDelay the
// probe blocks session launch for as long as the job lives.
func TestLoginShellPATHProbeDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	h := orphanpipetest.New(t)
	shell := h.WriteScript(t, "fake-shell", h.Spawn()+"\nwait\n")
	// Not parallel: the deadline seam is process-wide. The seam lets the test
	// fire the timeout only after h.AwaitStarted confirms the shell reached
	// h.Spawn(), so a loaded runner cannot kill the shell before the fixture
	// exists.
	armed := make(chan func(), 1)
	prevAfterFunc := loginShellPATHAfterFunc
	loginShellPATHAfterFunc = func(_ time.Duration, fire func()) func() bool {
		armed <- fire
		return func() bool { return true }
	}
	t.Cleanup(func() { loginShellPATHAfterFunc = prevAfterFunc })
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(30*time.Second, shell, "-lc", "echo $PATH")
		done <- probeRun{capture.view, err}
	}()
	awaitFixtureStarted(t, h, done)
	fire := <-armed
	fire()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("probe after its deadline ended = nil error, output %q", got.out)
	}
}

// TestLoginShellPATHProbePreservesAnswerWhenDeadlineFiresDuringDrain covers the
// successful side of the same bound: the shell prints PATH and exits 0 while a
// background job still holds stdout, so Wait spends WaitDelay draining the
// pipe. The timer fires in that window, but the process already answered; the
// probe must return the PATH rather than classify the drain as a timeout.
func TestLoginShellPATHProbePreservesAnswerWhenDeadlineFiresDuringDrain(t *testing.T) {
	h := orphanpipetest.New(t)
	pidFile := filepath.Join(t.TempDir(), "shell.pid")
	shell := h.WriteScript(t, "fake-shell", h.Spawn()+"\necho /from/login/shell\necho $$ > "+strconv.Quote(pidFile)+"\n")
	armed := make(chan func(), 1)
	prevAfterFunc := loginShellPATHAfterFunc
	loginShellPATHAfterFunc = func(_ time.Duration, fire func()) func() bool {
		armed <- fire
		return func() bool { return true }
	}
	t.Cleanup(func() { loginShellPATHAfterFunc = prevAfterFunc })
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(30*time.Second, shell, "-lc", "echo $PATH")
		done <- probeRun{capture.view, err}
	}()
	awaitFixtureStarted(t, h, done)
	waitForProcessExit(t, readShellPID(t, pidFile))
	fire := <-armed
	fire()
	got := orphanpipetest.Await(t, h, done)
	if got.err != nil {
		t.Fatalf("a shell that printed PATH and exited 0 was reported as failed: %v", got.err)
	}
	if string(got.out) != "/from/login/shell\n" {
		t.Fatalf("the probe's answer = %q, want the PATH the shell printed", got.out)
	}
}

// TestLoginShellPATHProbeFixtureStartupFailureEndsTheWait pins the failure side
// of fixture readiness: a shell that exits before spawning the pipe holder
// leaves h.AwaitStarted blocking, and only observing probe completion ends it.
func TestLoginShellPATHProbeFixtureStartupFailureEndsTheWait(t *testing.T) {
	h := orphanpipetest.New(t)
	shell := h.WriteScript(t, "fake-shell", "exit 0\n")
	capture, err := loginShellPATHOutput(5*time.Second, shell, "-lc", "echo $PATH")
	if err != nil {
		t.Fatalf("probe on a shell that exits without spawning: %v", err)
	}
	done := make(chan probeRun, 1)
	done <- probeRun{capture.view, nil}
	if err := waitForProbeFixture(h, done, 100*time.Millisecond); !errors.Is(err, errProbeFinishedBeforeFixture) {
		t.Fatalf("waitForProbeFixture on a probe that already finished = %v, want %v", err, errProbeFinishedBeforeFixture)
	}
}

func readShellPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if body, err := os.ReadFile(path); err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if convErr != nil {
				t.Fatalf("shell pid file %q = %q: %v", path, body, convErr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shell never wrote its pid to %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe shell %d liveness: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe shell %d did not exit", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The other side of the bound, and the everyday case: an rc file that starts a
// background job still lets the shell print PATH and exit 0, and that PATH is
// the probe's answer, not a failure.
//
// The probe's deadline is armed after the login shell exists (#4495), so
// process creation under load cannot kill it before it starts the grandchild
// this test's premise needs. The 30s bound leaves the grandchild time to
// announce itself; the bound this test proves is the probe's own WaitDelay.
func TestLoginShellPATHSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	shell := h.WriteScript(t, "fake-shell", h.Spawn()+"\necho /from/login/shell\n")
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(30*time.Second, shell, "-lc", "echo $PATH")
		done <- probeRun{capture.view, err}
	}()
	got := orphanpipetest.Await(t, h, done)
	if got.err != nil {
		t.Fatalf("the shell answered, but the probe returned %v", got.err)
	}
	if string(got.out) != "/from/login/shell\n" {
		t.Fatalf("the probe's answer = %q, want the PATH the shell printed", got.out)
	}
}
