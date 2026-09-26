//go:build unix

package orphanpipe_test

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

type captured struct {
	out []byte
	err error
}

// runOrphaning runs a script that writes "answer", leaves a grandchild on its
// output pipe, and exits with the given status.
func runOrphaning(t *testing.T, exitStatus string) (*exec.Cmd, captured) {
	t.Helper()
	h := orphanpipetest.New(t)
	script := h.WriteScript(t, "answers-and-orphans", "echo answer\n"+h.Spawn()+"\nexit "+exitStatus+"\n")
	cmd := exec.Command(script)
	cmd.WaitDelay = time.Second
	done := make(chan captured, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		done <- captured{out, err}
	}()
	return cmd, orphanpipetest.Await(t, h, done)
}

func TestChildErrReadsAnAnsweredChildAsSuccess(t *testing.T) {
	t.Parallel()
	cmd, got := runOrphaning(t, "0")
	if !errors.Is(got.err, exec.ErrWaitDelay) {
		t.Fatalf("CombinedOutput err = %v, want ErrWaitDelay: the staging no longer leaves the pipe held", got.err)
	}
	if err := orphanpipe.ChildErr(cmd, got.err); err != nil {
		t.Fatalf("ChildErr = %v, want nil for a child that exited 0", err)
	}
	if string(got.out) != "answer\n" {
		t.Fatalf("output = %q, want the child's complete answer", got.out)
	}
}

func TestChildErrKeepsAFailedChildsExit(t *testing.T) {
	t.Parallel()
	cmd, got := runOrphaning(t, "3")
	err := orphanpipe.ChildErr(cmd, got.err)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("ChildErr = %v, want the child's exit status 3", err)
	}
}

func TestChildErrPassesThroughOtherOutcomes(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("true")
	if err := orphanpipe.ChildErr(cmd, cmd.Run()); err != nil {
		t.Fatalf("ChildErr = %v, want nil for a clean run", err)
	}
	unstarted := exec.Command("true")
	startErr := errors.New("start failed")
	if err := orphanpipe.ChildErr(unstarted, startErr); !errors.Is(err, startErr) {
		t.Fatalf("ChildErr = %v, want the start error unchanged", err)
	}
}
