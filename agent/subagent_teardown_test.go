package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

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

// A child spawned into its own working directory owns a clone of the parent's
// environment. Its teardown at parent close must never Cleanup that clone (the
// clone shares the parent's process table), but it has to release the scratch
// the clone minted, both kinds. Unsandboxed is the default shape, and its
// scratch is the one an env mints lazily on its first command; a teardown that
// retains only the sandbox-provisioned kind holds this lease for the rest of
// the daemon's uptime.
func TestParentCloseReleasesAnOwnedUnsandboxedChildScratchLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock-based lease verification is unix-only")
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	parentLocal, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	// The shape prepareSubagentEnvironment builds for a working_dir child.
	childEnv := parentLocal.WithWorkingDirectory(t.TempDir())
	child, err := NewSession(client, parent.currentProfile(), childEnv, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the child's clone: %v", err)
	}
	// The child's first command is what mints its scratch and takes the lease.
	if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	scratch := childEnv.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the child minted no session scratch, so there is nothing to release")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the child's scratch lease is not held before the parent closes")
	}
	parent.subagents.track(&subagent{id: child.id, sess: child, ownsEnv: true, done: make(chan struct{})})

	parent.Close()

	if got := child.State(); got != SessionClosed {
		t.Errorf("owned child state after parent close = %q, want %q", got, SessionClosed)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("parent close removed the child's scratch %s, want it retained for the handoff: %v", scratch, err)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the child's scratch %s lease is still held after the parent closed", scratch)
	}
}

// Retained-terminal eviction closes a finished child to make room for a new
// spawn. A child with no working directory and no box of its own runs on the
// parent's very environment, and the parent is still working in it: eviction
// must close the child's own resources and nothing else — not the parent's
// in-flight processes, not the parent's scratch lease.
func TestEvictedTerminalChildLeavesTheParentEnvironmentAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process and flock probes are unix-only")
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}

	// A parent tool's in-flight process: a shell that reports its PID and then
	// waits to be released.
	signals := t.TempDir()
	pidPath := filepath.Join(signals, "pid")
	releasePath := filepath.Join(signals, "release")
	command := fmt.Sprintf("printf '%%s' $$ > %s; while test ! -e %s; do sleep 0.01; done",
		strconv.Quote(pidPath), strconv.Quote(releasePath))
	execDone := make(chan error, 1)
	go func() {
		_, err := shared.ExecCommand(context.Background(), command, 30_000, "", nil)
		execDone <- err
	}()
	release := func() { _ = os.WriteFile(releasePath, nil, 0o600) }
	t.Cleanup(release)
	var pid int
	waitForCondition(t, 5*time.Second, "the parent's process to report its PID", func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil || len(data) == 0 {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil
	})
	sharedScratch := shared.SessionScratchDir()
	if sharedScratch == "" {
		t.Fatal("the parent minted no session scratch, so there is nothing to protect")
	}
	if !scratchLeaseHeld(t, sharedScratch) {
		t.Fatal("the parent's scratch lease is not held while it is working")
	}

	// A finished child on the parent's own environment, exactly as a delegate
	// with neither a working dir nor a box of its own gets one.
	finished, err := NewSession(client, parent.currentProfile(), shared, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the parent's environment: %v", err)
	}
	ended := time.Now()
	done := make(chan struct{})
	close(done)
	terminal := &subagent{id: finished.id, sess: finished, status: SubagentCompleted, resultConsumed: true, endedAt: &ended, done: done}
	parent.cfg.testOnly.subagentReserveSlot = func(*Session) ([]*subagent, error) {
		return []*subagent{terminal}, nil
	}

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	prepared, err := parent.prepareSubagentRun(ctx, "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.disposeUnadopted()
	})

	if got := finished.State(); got != SessionClosed {
		t.Errorf("evicted child state = %q, want %q", got, SessionClosed)
	}
	select {
	case err := <-execDone:
		t.Fatalf("eviction ended the parent's in-flight process %d: %v", pid, err)
	default:
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the parent's in-flight process %d is gone after eviction: %v", pid, err)
	}
	if _, err := os.Stat(sharedScratch); err != nil {
		t.Errorf("eviction removed the parent's scratch %s: %v", sharedScratch, err)
	}
	if !scratchLeaseHeld(t, sharedScratch) {
		t.Errorf("eviction released the parent's scratch %s lease while the parent is still working in it", sharedScratch)
	}

	release()
	if err := <-execDone; err != nil {
		t.Fatalf("the parent's process after release: %v", err)
	}
}
