//go:build unix

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// startInFlightProcess runs a shell through env that reports its PID and then
// waits to be released, standing in for a parent tool's in-flight process. done
// receives ExecCommand's error once the shell exits; release lets it exit.
func startInFlightProcess(t *testing.T, env *execenv.LocalExecutionEnvironment) (pid int, done <-chan error, release func()) {
	t.Helper()
	signals := t.TempDir()
	pidPath := filepath.Join(signals, "pid")
	releasePath := filepath.Join(signals, "release")
	command := fmt.Sprintf("printf '%%s' $$ > %s; while test ! -e %s; do sleep 0.01; done",
		strconv.Quote(pidPath), strconv.Quote(releasePath))
	execDone := make(chan error, 1)
	go func() {
		_, err := env.ExecCommand(context.Background(), command, 30_000, "", nil)
		execDone <- err
	}()
	release = func() { _ = os.WriteFile(releasePath, nil, 0o600) }
	t.Cleanup(release)
	// The shell writes its PID within milliseconds of the spawn and nothing in
	// process signals that write, so this is a poll with a hang guard only.
	// TRIPWIRE: 30s sits orders of magnitude above the spawn-to-write time.
	waitForCondition(t, 30*time.Second, "the in-flight process to report its PID", func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil || len(data) == 0 {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil
	})
	return pid, execDone, release
}

// assertInFlightProcessSurvived fails the test if the process
// startInFlightProcess launched has been ended by what.
func assertInFlightProcessSurvived(t *testing.T, what string, pid int, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s ended the parent's in-flight process %d: %v", what, pid, err)
	default:
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the parent's in-flight process %d is gone after %s: %v", pid, what, err)
	}
}

// heldParentScratch returns the scratch the parent's environment is working in,
// failing unless the parent still holds its lease.
func heldParentScratch(t *testing.T, env *execenv.LocalExecutionEnvironment) string {
	t.Helper()
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the parent minted no session scratch, so there is nothing to protect")
	}
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the parent's scratch lease is not held while it is working")
	}
	return scratch
}

// assertParentScratchUntouched fails the test if what removed the parent's
// scratch or released the lease the parent is still holding.
func assertParentScratchUntouched(t *testing.T, what, scratch string) {
	t.Helper()
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("%s removed the parent's scratch %s: %v", what, scratch, err)
	}
	if !scratchLeaseHeld(t, scratch) {
		t.Errorf("%s released the parent's scratch %s lease while the parent is still working in it", what, scratch)
	}
}

// A child spawned into its own working directory owns a clone of the parent's
// environment. Its teardown at parent close must never Cleanup that clone (the
// clone shares the parent's process table), but it has to release the scratch
// the clone minted, both kinds. Unsandboxed is the default shape, and its
// scratch is the one an env mints lazily on its first command; a teardown that
// retains only the sandbox-provisioned kind holds this lease for the rest of
// the daemon's uptime.
func TestParentCloseReleasesAnOwnedUnsandboxedChildScratchLease(t *testing.T) {
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
	child.ownsEnv = true
	parent.subagents.track(&subagent{id: child.id, sess: child, done: make(chan struct{})})

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
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	pid, done, release := startInFlightProcess(t, shared)
	sharedScratch := heldParentScratch(t, shared)

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
	finishedDone := make(chan struct{})
	close(finishedDone)
	terminal := &subagent{id: finished.id, sess: finished, status: SubagentCompleted, resultConsumed: true, endedAt: &ended, done: finishedDone}
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
	assertInFlightProcessSurvived(t, "eviction", pid, done)
	assertParentScratchUntouched(t, "eviction", sharedScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the parent's process after release: %v", err)
	}
}

// Stable-controller reclamation closes retained terminal delegate runtimes to
// admit a new one. Like eviction it is a child teardown: the runtime's
// environment is the root's own or a clone sharing the root's process table,
// and the root is mid-create when this runs, so closing the runtime must not
// reach that environment.
func TestReclaimedDelegateRuntimeLeavesTheRootEnvironmentAlone(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	shared, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root.currentEnv())
	}
	pid, done, release := startInFlightProcess(t, shared)
	sharedScratch := heldParentScratch(t, shared)

	resident, err := NewSession(client, profile, shared, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the root's environment: %v", err)
	}
	root.delegateController.maxRetainedTerminal = 1
	seedDelegateReclaimRuntimeSession(t, root.delegateController, "dlg_old", "", time.Unix(5, 0).UTC(), false, false, resident)

	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatalf("reclaimDelegateRuntimeCapacity: %v", err)
	}

	if got := resident.State(); got != SessionClosed {
		t.Errorf("reclaimed runtime state = %q, want %q", got, SessionClosed)
	}
	assertInFlightProcessSurvived(t, "reclamation", pid, done)
	assertParentScratchUntouched(t, "reclamation", sharedScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}

// Disposing a stable delegate's isolation lane closes its resident child
// first. The child owns a clone rooted in the lane, and the clone shares the
// root's process table, so the disposal must not Cleanup it: the root is
// mid-tool when this runs. The child's own scratch is retained for the
// handoff like any other child close.
func TestDisposedLaneChildLeavesTheRootEnvironmentAlone(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	id, lanePath, _ := r.seedStableIsolationLane(t)
	rootLocal, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root.currentEnv())
	}
	childEnv := rootLocal.WithWorkingDirectory(lanePath)
	child, err := NewSession(root.client, root.currentProfile(), childEnv, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the lane clone: %v", err)
	}
	if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the lane clone: %v", err)
	}
	childScratch := childEnv.SessionScratchDir()
	if childScratch == "" {
		t.Fatal("the lane child minted no session scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(childScratch) })
	ended := time.Now()
	child.ownsEnv = true
	root.subagents.track(&subagent{id: "child-" + id, sess: child, status: SubagentFailed, endedAt: &ended, done: make(chan struct{})})
	pid, done, release := startInFlightProcess(t, rootLocal)
	rootScratch := heldParentScratch(t, rootLocal)

	if _, err := root.worktreeDispose(context.Background(), id, false, false); err != nil {
		t.Fatalf("worktreeDispose: %v", err)
	}

	if got := child.State(); got != SessionClosed {
		t.Errorf("disposed lane child state = %q, want %q", got, SessionClosed)
	}
	if laneWorktreePresent(lanePath) {
		t.Errorf("disposal retained the lane %s", lanePath)
	}
	if _, err := os.Stat(childScratch); err != nil {
		t.Errorf("disposal removed the child's scratch %s, want it retained for the handoff: %v", childScratch, err)
	}
	if scratchLeaseHeld(t, childScratch) {
		t.Errorf("the child's scratch %s lease is still held after the disposal", childScratch)
	}
	assertInFlightProcessSurvived(t, "lane disposal", pid, done)
	assertParentScratchUntouched(t, "lane disposal", rootScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}

// The stable controller's own close policy tears down every resident runtime
// it still holds. Those runtimes are child sessions on the root's environment
// or on clones sharing its process table, so the policy is a child teardown
// like every other: it must not reach the root's environment.
func TestControllerCloseRuntimeTreeLeavesTheRootEnvironmentAlone(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	root := &Session{delegateController: c}
	c.rootRuntime = root
	// The environment the resident runs on stands in for the root's own: the
	// resident does not own it, and the root is still working in it.
	shared := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(shared.Cleanup)
	pid, done, release := startInFlightProcess(t, shared)
	sharedScratch := heldParentScratch(t, shared)

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	resident, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), shared, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the shared environment: %v", err)
	}
	seedDelegateReclaimRuntimeSession(t, c, "dlg_resident", "", time.Unix(5, 0).UTC(), false, false, resident)

	if err := c.closeRuntimeTree(context.Background()); err != nil {
		t.Fatalf("closeRuntimeTree: %v", err)
	}

	if got := resident.State(); got != SessionClosed {
		t.Errorf("resident runtime state after the controller close = %q, want %q", got, SessionClosed)
	}
	assertInFlightProcessSurvived(t, "the controller close", pid, done)
	assertParentScratchUntouched(t, "the controller close", sharedScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}

// A stable delegate's runtime can be resident in the controller's tree while
// the owner's subagent map never tracked it: the create path attaches the
// runtime to the controller before the owner adopts the child, so a start that
// loses to a concurrent stop leaves the controller holding a runtime no map
// entry names. Such a runtime still owns its clone's scratch, and the root's
// close of the runtime tree is the only teardown it ever gets: it has to
// release the lease and keep the directory, like every other child teardown.
func TestRootCloseReleasesAnUntrackedResidentRuntimeScratchLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	shared := execenv.NewLocalExecutionEnvironment(t.TempDir())
	t.Cleanup(shared.Cleanup)
	root := &Session{delegateController: c, ownsDelegateController: true, env: shared}
	c.rootRuntime = root
	pid, done, release := startInFlightProcess(t, shared)
	sharedScratch := heldParentScratch(t, shared)

	// The shape prepareSubagentEnvironment builds for a working_dir delegate.
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	childEnv := shared.WithWorkingDirectory(t.TempDir())
	resident, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), childEnv, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the delegate's clone: %v", err)
	}
	resident.ownsEnv = true
	if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the delegate's clone: %v", err)
	}
	scratch := childEnv.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the resident runtime minted no session scratch, so there is nothing to release")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the resident runtime's scratch lease is not held before the root closes")
	}
	// Resident in the tree, and named by no entry in the root's subagent map.
	seedDelegateReclaimRuntimeSession(t, c, "dlg_untracked", "", time.Unix(5, 0).UTC(), false, false, resident)

	if err := root.closeOwnedDelegateRuntimeTree(context.Background()); err != nil {
		t.Fatalf("closeOwnedDelegateRuntimeTree: %v", err)
	}

	if got := resident.State(); got != SessionClosed {
		t.Errorf("untracked resident runtime state after the root close = %q, want %q", got, SessionClosed)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("the root close removed the runtime's scratch %s, want it retained for the handoff: %v", scratch, err)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the untracked runtime's scratch %s lease is still held after the root closed", scratch)
	}
	assertInFlightProcessSurvived(t, "the root's delegate tree close", pid, done)
	assertParentScratchUntouched(t, "the root's delegate tree close", sharedScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}

// A delegate with neither a working dir nor a box of its own runs on the
// parent's very environment: prepareSubagentRun hands the child that env
// untouched, and the parent is still working in it. The child has to record
// that it does NOT own it, because that record is the whole gate on the
// teardown's environment step — a wrong "owned" releases the parent's live
// scratch lease and retires the file-tool layers its in-flight tools use.
func TestSpawnedChildOnTheParentEnvironmentRecordsNoOwnership(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	pid, done, release := startInFlightProcess(t, shared)
	sharedScratch := heldParentScratch(t, shared)

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	prepared, err := parent.prepareSubagentRun(ctx, "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.disposeUnadopted()
	})
	child := prepared.sub.sess
	if child.currentEnv() != parent.currentEnv() {
		t.Fatal("the spawned child did not land on the parent's environment; this test would prove nothing")
	}
	if child.ownsEnv {
		t.Error("a child spawned onto the parent's environment recorded that it owns it")
	}

	teardownChildSession(context.Background(), child, retainChildScratch)

	if got := child.State(); got != SessionClosed {
		t.Errorf("child state after its teardown = %q, want %q", got, SessionClosed)
	}
	assertInFlightProcessSurvived(t, "the shared child's teardown", pid, done)
	assertParentScratchUntouched(t, "the shared child's teardown", sharedScratch)

	release()
	if err := <-done; err != nil {
		t.Fatalf("the parent's process after release: %v", err)
	}
}

// restoredCloneRuntime is a committed delegate brought up through the
// production restore path, with everything the ownership pins measure against
// it: the runtime, the clone the restore re-rooted for it, that clone's own
// scratch, and a root still working — an in-flight process and a held scratch
// of its own — so a teardown that reaches the root's environment shows up.
type restoredCloneRuntime struct {
	runtime     *Session
	scratch     string
	rootScratch string
	pid         int
	done        <-chan error
	release     func()
}

// restoreDelegateRuntimeOnAClone restores a root from a cold committed
// delegate and hands its runtime back through restoreIdle. It fails the test
// unless the runtime really did land on a clone of its own holding a scratch
// no one else holds, since that is the premise every assertion about ownership
// rests on. The root's close is registered FIRST so it runs LAST: the
// committed lease has to be failed before the close, or closeRuntimeTree
// drains a still-committed stop for its whole budget.
func restoreDelegateRuntimeOnAClone(t *testing.T) restoredCloneRuntime {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	rootLocal, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root.currentEnv())
	}
	pid, done, release := startInFlightProcess(t, rootLocal)
	rootScratch := heldParentScratch(t, rootLocal)

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("restoreIdle reused a resident child; this test would prove nothing")
	}
	t.Cleanup(func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	})

	runtime := sub.sess
	clone, ok := runtime.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restored runtime env = %T, want a local environment", runtime.currentEnv())
	}
	if clone == rootLocal {
		t.Fatal("the restore reused the root's environment; this test would prove nothing")
	}
	scratch := clone.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the restored runtime holds no session scratch, so there is nothing to settle")
	}
	if scratch == rootScratch {
		t.Fatal("the restored runtime holds the root's scratch; this test would prove nothing")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the restored runtime's scratch lease is not held before its teardown")
	}
	return restoredCloneRuntime{runtime: runtime, scratch: scratch, rootScratch: rootScratch, pid: pid, done: done, release: release}
}

// A restored stable delegate never shares its root's environment: its committed
// descriptor carries a working dir, so the restore re-roots a clone for it and
// the clone owns whatever scratch it mints. The runtime has to record that, or
// its teardown leaves the clone's lease held for the rest of the daemon's
// uptime — and it must record it without reaching the root's environment, which
// the clone shares a process table with while the root is mid-restore.
func TestRestoredDelegateRuntimeOwnsItsCloneScratch(t *testing.T) {
	r := restoreDelegateRuntimeOnAClone(t)
	if !r.runtime.ownsEnv {
		t.Error("a restored delegate on a clone of its own recorded that it does not own it")
	}

	teardownChildSession(context.Background(), r.runtime, retainChildScratch)

	if got := r.runtime.State(); got != SessionClosed {
		t.Errorf("restored runtime state after its teardown = %q, want %q", got, SessionClosed)
	}
	if _, err := os.Stat(r.scratch); err != nil {
		t.Errorf("the teardown removed the clone's scratch %s, want it retained for the handoff: %v", r.scratch, err)
	}
	if scratchLeaseHeld(t, r.scratch) {
		t.Errorf("the clone's scratch %s lease is still held after the runtime's teardown", r.scratch)
	}
	assertInFlightProcessSurvived(t, "the restored runtime's teardown", r.pid, r.done)
	assertParentScratchUntouched(t, "the restored runtime's teardown", r.rootScratch)

	r.release()
	if err := <-r.done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}

// A discarded restore candidate makes the same ownership decision in the
// dispose direction: nothing adopted it, so its clone's scratch goes with it
// rather than being handed off. The record has to be right for that too — a
// candidate that believed it shared the root's environment would leak the dir
// and its lease with no owner left to settle either.
func TestDiscardedRestoreCandidateDisposesItsCloneScratch(t *testing.T) {
	r := restoreDelegateRuntimeOnAClone(t)

	r.runtime.discardRestoredCandidate()

	if _, err := os.Stat(r.scratch); !os.IsNotExist(err) {
		t.Errorf("the discarded candidate retained its clone's scratch %s: stat err = %v", r.scratch, err)
	}
	assertInFlightProcessSurvived(t, "the candidate's discard", r.pid, r.done)
	assertParentScratchUntouched(t, "the candidate's discard", r.rootScratch)

	r.release()
	if err := <-r.done; err != nil {
		t.Fatalf("the root's process after release: %v", err)
	}
}
