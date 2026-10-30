//go:build unix

package agent

// Live-owner preservation cases for dead-owner delegate lane collection (issue
// #5471). These hold a real flock on the owner root session's livelock file —
// the exact state a live session process keeps — and pin the contract the
// recovery must not break: a live delegate's lane is never pruned.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/rendezvous"
)

// holdLivelock takes the flock a live root session process would hold on its
// livelock file, returning the release func. The file is created if absent.
func holdLivelock(t *testing.T, stateDir, sid string) (release func()) {
	t.Helper()
	path := filepath.Join(stateDir, "sessions", sid+".livelock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open livelock %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatalf("flock livelock %s: %v", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// TestWorktreePrune_LiveOwnerDelegateLaneStaysLocked: while the owner root
// session's process is alive (its livelock flock is held), the delegate lane's
// lock is exactly as binding as before — prune reports it locked and touches
// nothing. This is the preservation half of issue #5471's contract.
func TestWorktreePrune_LiveOwnerDelegateLaneStaysLocked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	release := holdLivelock(t, r.stateDir, deadOwnerRootSID)
	defer release()
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal that keeps the lane untouched", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("live-owner lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("live-owner lane branch was deleted")
	}
}

// TestWorktreePrune_NestedOwnerLiveRootStaysLocked: a nested delegate lane's
// marker names a subagent session that may not be instantiated at all while
// its root process is alive (a restored-idle delegate, exactly the state
// between a root's resume and its next delegate_send). Only the root proves
// the tree's liveness, so the lane must stay locked while the root holds its
// livelock even though the named subagent owns no live process record itself.
func TestWorktreePrune_NestedOwnerLiveRootStaysLocked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	release := holdLivelock(t, r.stateDir, deadOwnerRootSID)
	defer release()
	// The subagent session has a meta (it exists in the tree) but no livelock
	// of its own — only roots host processes.
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerSubSID, true, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerSubSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerSubSID

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal: the lane's tree root is live", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("nested live-root lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("nested live-root lane branch was deleted")
	}
}

// reapedChildPID returns the pid of a child process that has already exited
// and been reaped — deterministically dead, unlike a hardcoded pid.
func reapedChildPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start probe child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // reap: the pid is now provably gone
	return pid
}

// TestWorktreePrune_RendezvousDeadDaemonLaneCollected: the rendezvous arm. A
// serve daemon's entry names the root session id and a pid that no longer
// exists — a crashed daemon's residue (a graceful shutdown removes its entry).
// With no livelock (the daemon predates them, or died before taking one),
// that dead-pid entry alone is the proof the owner is gone.
func TestWorktreePrune_RendezvousDeadDaemonLaneCollected(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	rv := t.TempDir()
	r.s.worktreeRendezvousDir = rv
	if _, err := rendezvous.Write(rv, rendezvous.Entry{
		PID: reapedChildPID(t), SessionID: deadOwnerRootSID, ThreadID: deadOwnerRootSID,
	}); err != nil {
		t.Fatalf("write rendezvous entry: %v", err)
	}
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e == nil {
		t.Fatalf("%s not collected; skipped: %+v", deadOwnerDlgID, pruneEntries(t, out, "skipped"))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("dead-daemon lane worktree survived prune: err=%v", statErr)
	}
}

// TestWorktreePrune_RendezvousLiveDaemonLaneStaysLocked: the same entry shape
// with a live pid — the daemon is running, so its delegate lanes are exactly
// as locked as before, whatever the livelock says.
func TestWorktreePrune_RendezvousLiveDaemonLaneStaysLocked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	rv := t.TempDir()
	r.s.worktreeRendezvousDir = rv
	if _, err := rendezvous.Write(rv, rendezvous.Entry{
		PID: os.Getpid(), SessionID: deadOwnerRootSID, ThreadID: deadOwnerRootSID,
	}); err != nil {
		t.Fatalf("write rendezvous entry: %v", err)
	}
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal that keeps the lane untouched", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("live-daemon lane lost its lock")
	}
}

// TestWorktreePrune_LiveLivelockOutranksDeadRendezvousEntry: a session resumed
// by a new process takes the livelock before its serve daemon rewrites its
// rendezvous entry; the held livelock must win over the stale dead-pid entry.
func TestWorktreePrune_LiveLivelockOutranksDeadRendezvousEntry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	rv := t.TempDir()
	r.s.worktreeRendezvousDir = rv
	if _, err := rendezvous.Write(rv, rendezvous.Entry{
		PID: reapedChildPID(t), SessionID: deadOwnerRootSID, ThreadID: deadOwnerRootSID,
	}); err != nil {
		t.Fatalf("write rendezvous entry: %v", err)
	}
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	release := holdLivelock(t, r.stateDir, deadOwnerRootSID)
	defer release()
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal: the held livelock outranks the stale entry", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("live (livelock-held) lane lost its lock")
	}
}

// TestWorktreePrune_DeadOwnerReleaseRefusesChangedMarker: the fresh listing
// that confirmed the delegate marker and the release that follows are separate
// steps, so a revived owner (or a human following the documented manual
// recovery) can swap the lock in between. The release must act on the marker
// it actually removes — a lane taken by another owner is skipped, untouched.
func TestWorktreePrune_DeadOwnerReleaseRefusesChangedMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	foreign := worktree.FormatSessionMarker("034anotherliveowner")

	r.s.worktreeBeforeStrandedMarkerRelease = func(lanePath string) {
		wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
		wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreign, lanePath)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("prune collected a lane another owner had taken: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	if reason, _ := e["reason"].(string); reason != "lock changed since listing" {
		t.Errorf("reason = %q, want lock changed since listing", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, foreign) {
		t.Error("the replacement owner's lock was displaced")
	}
}

// TestWorktreePrune_DeadOwnerReleaseRestoresMarkerOnCollectFailure: the
// release was made for a removal that then failed; the dead owner's delegate
// marker goes back so the lane is exactly as protected as before the pass.
func TestWorktreePrune_DeadOwnerReleaseRestoresMarkerOnCollectFailure(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	withStrandedLaneRunner(t, r, func(inner worktree.GitRunner, args []string) (string, error, bool) {
		if len(args) >= 4 && args[0] == "worktree" && args[1] == "remove" &&
			filepath.Clean(args[len(args)-1]) == filepath.Clean(path) {
			return "", context.Canceled, true
		}
		return "", nil, false
	})

	if _, err := r.pruneOp(t); err == nil {
		t.Fatal("prune reported success despite the injected cancellation")
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("dead-owner lane lost its lock to a collection that then failed")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("lane branch was deleted despite the failed collection")
	}
}
