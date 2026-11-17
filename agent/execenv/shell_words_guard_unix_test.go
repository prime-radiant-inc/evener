//go:build linux || darwin

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// TestExecCommandKeepsTheShellForANonRegularProgram proves finding 6. An
// executable-mode FIFO passes a permission check and then blocks in os.Open,
// which runs before any timeout or cancellation exists to release it. bash
// refuses a FIFO as a command (126), so the fast path must refuse it too — and
// must refuse it without opening it.
func TestExecCommandKeepsTheShellForANonRegularProgram(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX file types do not apply on windows")
	}

	root := t.TempDir()
	pipe := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(pipe, 0o755); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A symlink to a regular, loadable program must still be followed: the check
	// refuses the file type, not the indirection.
	writeExecFixture(t, filepath.Join(root, "real"), "#!/bin/sh\nexit 0\n")
	linkToProgram := filepath.Join(root, "link-to-program")
	if err := os.Symlink(filepath.Join(root, "real"), linkToProgram); err != nil {
		t.Fatal(err)
	}

	if kernelLoadsProgram(pipe) {
		t.Fatal("kernelLoadsProgram accepted a FIFO")
	}
	if !kernelLoadsProgram(linkToProgram) {
		t.Fatal("kernelLoadsProgram refused a symlink to a regular executable")
	}

	// The runtime choice must return without blocking in an open, because that
	// open would happen before any timeout or cancellation exists to release it.
	// bash does not run a FIFO as a program either: with no writer it blocks too,
	// but its block is inside a child the timeout and cancellation can kill,
	// which is exactly what keeping the shell preserves. So the assertion is on
	// the choice, not on a run that would hang on both sides.
	env, factory := guardEnv(t, root, fixturePathEnv("/usr/local/bin", "/usr/bin", "/bin"))
	choice := make(chan struct{})
	go func() {
		defer close(choice)
		env.commandRuntimeForCommand("./pipe", root, env.launchEnv(root, nil))
	}()
	select {
	case <-choice:
	case <-time.After(30 * time.Second):
		t.Fatal("commandRuntimeForCommand(./pipe) did not return: the choice blocked on the FIFO")
	}
	if len(factory.argv) != 0 {
		t.Fatalf("a FIFO program ran as argv %q, want the shell", factory.argv)
	}

	// The symlink to a real program takes the fast path.
	factory.shell, factory.argv = nil, nil
	if _, err := env.ExecCommand(context.Background(), "./link-to-program", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand(./link-to-program): %v", err)
	}
	if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], []string{"./link-to-program"}) {
		t.Fatalf("argv = %q, want the symlinked program on the fast path", factory.argv)
	}
}
