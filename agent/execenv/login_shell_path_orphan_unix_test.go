//go:build unix

package execenv

import (
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

type probeRun struct {
	out []byte
	err error
}

// The login-shell probe's only bound is loginShellPATHTimeout, and it runs the
// user's rc chain, which may background anything (an agent, a daemon, a
// `something &` line). That job inherits the probe's stdout, and killing the
// shell when the timeout ends does not kill it, so without a WaitDelay the
// probe blocks session launch for as long as the job lives.
func TestLoginShellPATHProbeDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	shell := h.WriteScript(t, "fake-shell", h.Spawn()+"\nwait\n")
	// The probe's own deadline is the only bound this test gives it; it ends
	// once the shell is killed and WaitDelay drains the inherited pipe.
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(200*time.Millisecond, shell, "-lc", "echo $PATH")
		done <- probeRun{capture.view, err}
	}()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("probe after its deadline ended = nil error, output %q", got.out)
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
