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
		out, err := loginShellPATHOutput(ctx, shell, "-lc", "echo $PATH")
		done <- probeRun{out, err}
	}()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("probe after its context ended = nil error, output %q", got.out)
	}
}

// The other side of the bound, and the everyday case: an rc file that starts a
// background job still lets the shell print PATH and exit 0, and that PATH is
// the probe's answer, not a failure.
func TestLoginShellPATHSurvivesAnOrphanedPipeHolder(t *testing.T) {
	resetLoginShellPATHCache(t)
	h := orphanpipetest.New(t)
	t.Setenv("SHELL", h.WriteScript(t, "fake-shell", h.Spawn()+"\necho /from/login/shell\n"))
	done := make(chan string, 1)
	go func() { done <- resolveLoginShellPATH() }()
	if got := orphanpipetest.Await(t, h, done); got != "/from/login/shell" {
		t.Fatalf("resolveLoginShellPATH() = %q, want the PATH the shell printed", got)
	}
}
