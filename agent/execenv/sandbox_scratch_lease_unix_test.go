//go:build unix

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestCleanupReleasesUnsandboxedScratchLease is the leak regression from the
// task-1 review: off/unsandboxed is the DEFAULT sandbox mode, and
// session_lifecycle.go calls Cleanup() on every session close, so if Cleanup
// never released the unsandboxedScratch lease, a long-running `evener serve`
// daemon would hold one open file descriptor (the lease flock) per session
// for the rest of the process's life — sweepCrashedSessionScratch can only
// reclaim a lease that is currently acquirable. Verified at the OS level: a
// fresh flock attempt on the SAME lease file must succeed immediately after
// Cleanup(), proving the held lock was actually released, not merely that no
// error was returned.
func TestCleanupReleasesUnsandboxedScratchLease(t *testing.T) {
	worktree := t.TempDir()
	env := NewLocalExecutionEnvironment(worktree)
	scratch := env.unsandboxedScratchDir()
	if scratch == "" {
		t.Fatal("unsandboxedScratchDir provisioning failed")
	}

	env.Cleanup()

	if scratchLeaseHeld(t, scratch) {
		t.Fatal("lease still held after Cleanup (leak)")
	}
}

// scratchLeaseHeld reports whether a live owner still holds dir's scratch lease
// by trying the real OS-level lock: there is no exported way to introspect
// lease state. ".evener-session.lock" mirrors sandbox.SessionScratch's lease
// filename convention (agent/sandbox/session_scratch.go).
func scratchLeaseHeld(t *testing.T, dir string) bool {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".evener-session.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open scratch lease in %s: %v", dir, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true
		}
		t.Fatalf("probe scratch lease in %s: %v", dir, err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// A session's teardown hands its scratch to the human: the leases go, the
// directories stay. An env can hold two — the one EnableSandbox provisioned and
// the one an unsandboxed spawn minted — and a teardown that runs neither
// Cleanup (a child whose process table belongs to its parent) nor a disposal
// has to release both together, or the one it misses is held for the rest of
// the daemon's uptime.
func TestRetainSessionScratchReleasesBothLeasesAndKeepsBothDirs(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { env.Cleanup(); env.DisposeUnadoptedScratch() })

	_ = env.commandEnvironment(nil)
	unsandboxed := env.SessionScratchDir()
	if unsandboxed == "" {
		t.Fatal("an unsandboxed env minted no session scratch, so there is nothing to retain")
	}
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	owned := env.SessionScratchDir()
	if owned == "" || owned == unsandboxed {
		t.Fatalf("owned scratch = %q, want one of its own beside the unsandboxed %q", owned, unsandboxed)
	}
	dirs := map[string]string{"unsandboxed": unsandboxed, "owned": owned}
	for name, dir := range dirs {
		if !scratchLeaseHeld(t, dir) {
			t.Fatalf("%s scratch %s lease is not held before the retain", name, dir)
		}
	}

	env.RetainSessionScratch()

	for name, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s scratch %s did not survive the retain: %v", name, dir, err)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("%s scratch %s lease is still held after the retain", name, dir)
		}
	}
}

// A session that swaps its environment for a re-rooted clone (a worktree
// re-entry on resume) keeps working in the scratch its original environment
// provisioned: the clone's re-rooted kernel wrapper carries the same session
// tmp, and an unsandboxed clone exports whatever it is handed. Ownership has to
// follow the swap — exactly one environment owns each scratch, and it must be
// the one the session's teardown reaches — or the original's leases are held
// for the rest of the daemon's uptime while the clone's teardown releases
// nothing.
func TestAdoptSessionScratchMovesBothKindsToTheClone(t *testing.T) {
	original := NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(func() { original.Cleanup(); original.DisposeUnadoptedScratch() })
	_ = original.commandEnvironment(nil)
	unsandboxed := original.SessionScratchDir()
	if unsandboxed == "" {
		t.Fatal("an unsandboxed env minted no session scratch, so there is nothing to adopt")
	}
	if err := original.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	owned := original.SessionScratchDir()
	if owned == "" || owned == unsandboxed {
		t.Fatalf("owned scratch = %q, want one of its own beside the unsandboxed %q", owned, unsandboxed)
	}
	dirs := map[string]string{"unsandboxed": unsandboxed, "owned": owned}
	clone := original.WithWorkingDirectory(t.TempDir())
	t.Cleanup(clone.DisposeUnadoptedScratch)
	if got := clone.SessionScratchDir(); got != "" {
		t.Fatalf("a fresh clone reports scratch %q, want none of its own", got)
	}

	clone.AdoptSessionScratch(original)

	if got := clone.SessionScratchDir(); got != owned {
		t.Errorf("clone scratch = %q after the adoption, want the original's owned %q", got, owned)
	}
	// The original owns neither any more: disposing it drops nothing, and the
	// leases stay held (by the clone now).
	original.DisposeUnadoptedScratch()
	for name, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s scratch %s went with the original's disposal: %v", name, dir, err)
		}
		if !scratchLeaseHeld(t, dir) {
			t.Errorf("%s scratch %s lease was released by the original's disposal", name, dir)
		}
	}
	// The clone owns both: disposing it drops both, leases included.
	clone.DisposeUnadoptedScratch()
	for name, dir := range dirs {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s scratch %s survived the clone's disposal: stat err = %v", name, dir, err)
		}
	}
}
