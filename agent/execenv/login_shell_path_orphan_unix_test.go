//go:build unix

package execenv

import (
	"context"
	"testing"

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		h.AwaitStarted()
		cancel()
	}()
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(ctx, shell, "-lc", "echo $PATH")
		done <- probeRun{capture.view, err}
	}()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("probe after its context ended = nil error, output %q", got.out)
	}
}

// The other side of the bound, and the everyday case: an rc file that starts a
// background job still lets the shell print PATH and exit 0, and that PATH is
// the probe's answer, not a failure.
//
// The probe's context is the test's, not resolveLoginShellPATH's
// loginShellPATHTimeout. That deadline is armed before the login shell exists,
// so on a loaded host it can kill the shell before it starts the grandchild
// this test's premise needs — the pipe would close with the shell, and the test
// would report the absent premise instead of the survival it means to prove
// (issue #4127). The test cancels nothing, so its context must not expire; the
// bound it proves is the probe's own WaitDelay.
func TestLoginShellPATHSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	shell := h.WriteScript(t, "fake-shell", h.Spawn()+"\necho /from/login/shell\n")
	done := make(chan probeRun, 1)
	go func() {
		capture, err := loginShellPATHOutput(context.Background(), shell, "-lc", "echo $PATH")
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
