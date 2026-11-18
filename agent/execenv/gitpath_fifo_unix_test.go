//go:build unix

package execenv

import (
	"syscall"
	"testing"
	"time"
)

// TestStructuralWorktreeRootDoesNotBlockOnAFifoGitEntry is the high: a .git
// ENTRY that is not a directory is a pointer file, and the resolution reads it
// before the git subprocess (and its timeout) is installed. A plain os.ReadFile
// there follows whatever the entry now names, so an entry that became a FIFO
// blocks indefinitely — and the callers reach it while holding the session
// mutex, which wedges request preparation rather than one slow resolution.
//
// The assertion is on REACHING an answer, not on what the answer is: a FIFO with
// no writer is exactly the shape that hangs an unbounded read, so the property
// under test is that the call returns at all. An unbounded implementation
// fails this test by TIMING OUT.
func TestStructuralWorktreeRootDoesNotBlockOnAFifoGitEntry(t *testing.T) {
	dir := t.TempDir()
	gitPath := dir + "/.git"
	if err := syscall.Mkfifo(gitPath, 0o600); err != nil {
		t.Skipf("this platform cannot create a named pipe: %v", err)
	}

	type result struct {
		root     string
		answered bool
	}
	done := make(chan result, 1)
	go func() {
		root, answered := structuralWorktreeRoot(dir)
		done <- result{root: root, answered: answered}
	}()

	select {
	case got := <-done:
		// No structural verdict, so the caller falls through to git, which runs
		// under its own timeout and reports its own answer. What must NOT happen
		// is a negative being settled here from a read that never completed.
		if got.answered {
			t.Errorf("a FIFO .git entry produced a structural verdict %q; an unreadable pointer must answer unknown", got.root)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: an unbounded pointer read, not a slow filesystem
		t.Fatal("resolving a FIFO .git entry blocked: the pointer file is read without a bound")
	}
}

// TestGitRootOrEmptyDoesNotBlockOnAFifoGitEntry is the same property one level
// up, at the function the session actually calls. It covers the composed path:
// the structural probe must not hang, and the git fallback it hands off to is
// bounded on its own.
func TestGitRootOrEmptyDoesNotBlockOnAFifoGitEntry(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(dir+"/.git", 0o600); err != nil {
		t.Skipf("this platform cannot create a named pipe: %v", err)
	}
	env := NewLocalExecutionEnvironment(dir)

	done := make(chan string, 1)
	go func() {
		root, _ := GitRootOrEmptyFreshDefinitive(env, dir)
		done <- root
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second): // TRIPWIRE: the whole resolution, git fallback included
		t.Fatal("GitRootOrEmptyFreshDefinitive blocked on a FIFO .git entry")
	}
}
