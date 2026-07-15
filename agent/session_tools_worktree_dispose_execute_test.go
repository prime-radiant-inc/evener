package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// seedIsolationLaneOn seeds a real isolation delegate lane owned by sess (the
// session-parameterized core of wtRepo.seedIsolationLane), so a coordinator child
// session can be given its own grandchild lanes for the nested-cascade test.
func seedIsolationLaneOn(t *testing.T, sess *Session) (delegateID, lanePath string) {
	t.Helper()
	delegateID = jobstore.NewDelegateID()
	path, _, _, _, _, err := sess.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree on %s: %v", sess.ID(), err)
	}
	jobID := jobstore.NewJobID()
	now := time.Now().UTC()
	ref := encodeRef("", "gchild-"+delegateID)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:          1,
		ChildSessionID:   "gchild-" + delegateID,
		TranscriptRef:    ref,
		ParentSessionID:  sess.ID(),
		ParentJobID:      jobID,
		OwnerSessionID:   sess.ID(),
		VisibleSessionID: sess.ID(),
		WorkingDir:       path,
		LocalEnvPolicy:   "default",
		Isolation:        "worktree",
	}
	if err := sess.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       delegateID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append grandchild start: %v", err)
	}
	if err := sess.jobManager.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusStopped,
		Reason:      "runtime_lost",
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("append grandchild finished: %v", err)
	}
	return delegateID, path
}

// These are REAL-git unit tests for dispose EXECUTION (spec §P1 steps 7-8):
// retained-child eviction, the remove ladder (gone / present-dirty / re-lock
// failure), and the nested-coordinator cascade budget.

// trackRetainedIsolationChild builds a real, quiescent child *Session rooted at
// the lane and tracks it as a retained subagent keyed by the delegate id, so a
// dispose op sees a live retained child to evict (spec §P1 step 7). ownsEnv=true
// mirrors a per-lane re-rooted delegate whose env the disposer must Cleanup().
func (r *wtRepo) trackRetainedIsolationChild(t *testing.T, delegateID, lanePath string) *Session {
	t.Helper()
	child := newSession(t, withDir(lanePath), withoutGitSnapshot())
	r.s.subagents.track(&subagent{
		id:      delegateID,
		sess:    child,
		ownsEnv: true,
		done:    make(chan struct{}),
	})
	return child
}

func (r *wtRepo) delegateResumability(t *testing.T, id string) delegateResumability {
	t.Helper()
	recs, err := r.s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	rec, desc := findDelegateLaneRecord(recs, id)
	if desc == nil {
		t.Fatalf("no delegate lane record for %s", id)
	}
	return r.s.assessDelegateResumability(rec, delegateResumabilityProjection)
}

// TestDispose_EvictsRetainedChild is spec test 12: a retained quiescent child is
// gated, evicted (closed + removed from the table), its lane removed and marked
// disposed, and a later delegate_send hits the disposed refusal (restore path).
func TestDispose_EvictsRetainedChild(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	child := r.trackRetainedIsolationChild(t, id, lanePath)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed {
		t.Fatal("fresh disposal reported already-disposed")
	}

	// Child evicted: removed from the subagent table.
	if r.s.subagents.get(id) != nil {
		t.Error("retained child still in the subagent table after eviction")
	}
	// Child session closed (its close ran).
	if child.State() != SessionClosed {
		t.Errorf("evicted child state = %v, want %v", child.State(), SessionClosed)
	}
	// Lane removed + marked disposed + branch gone.
	if laneWorktreePresent(lanePath) {
		t.Error("lane not removed after eviction")
	}
	if !r.disposedEventPresent(t, id) {
		t.Error("disposed mark absent after eviction")
	}
	if r.branchExists(t, id) {
		t.Error("branch not deleted after eviction")
	}
	// delegate_send now hits the disposed refusal via the restore path.
	if a := r.delegateResumability(t, id); a.Resumable || a.Reason != notResumableWorktreeDisposed {
		t.Errorf("post-dispose resumability = %+v, want disposed refusal", a)
	}
}

// TestDispose_RemoveRefused_LaneGone_MarkedDisposed is spec test 13 (gone arm):
// a concurrent collector removes the lane between our unlock and our remove; the
// non-force remove fails, we stat GONE and finish the disposal bookkeeping.
func TestDispose_RemoveRefused_LaneGone_MarkedDisposed(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	// Simulate a concurrent collector winning the remove: after our unlock, before
	// our remove, the lane directory disappears.
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		wtGit(t, r.mainRoot, "worktree", "remove", "--force", p)
	}

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed {
		t.Fatal("gone-arm disposal reported already-disposed")
	}
	if laneWorktreePresent(lanePath) {
		t.Error("lane still present in gone arm")
	}
	if !r.disposedEventPresent(t, id) {
		t.Error("disposed mark absent in gone arm")
	}
	if r.branchExists(t, id) {
		t.Error("leftover branch not cleaned in gone arm")
	}
}

// TestDispose_RemoveRefused_PresentDirty_KeptAfterEviction is spec test 13
// (present arm): a late dirty write races the clean check, the non-force remove
// refuses, and the lane is downgraded to KEPT-after-eviction — re-locked with the
// disposer's own marker, NOT marked disposed, still resumable.
func TestDispose_RemoveRefused_PresentDirty_KeptAfterEviction(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed {
		t.Fatal("kept-after-eviction reported already-disposed")
	}
	if !laneWorktreePresent(lanePath) {
		t.Error("KEPT-after-eviction lane was removed")
	}
	if r.disposedEventPresent(t, id) {
		t.Error("KEPT-after-eviction lane wrongly marked disposed")
	}
	// Re-locked with the disposer's own serf:dlg marker.
	reg, locked, reason := r.laneLocked(t, lanePath)
	if !reg || !locked {
		t.Fatalf("KEPT-after-eviction lane not re-locked (reg=%t locked=%t)", reg, locked)
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != id {
		t.Errorf("re-lock reason = %q, want serf:dlg marker for %s", reason, id)
	}
	// Not disposed-refused: the descriptor was never marked, so the delegate is
	// still resumable via the restore path (no Disposed reason).
	if a := r.delegateResumability(t, id); a.Reason == notResumableWorktreeDisposed {
		t.Errorf("KEPT-after-eviction wrongly reports a disposed refusal: %+v", a)
	}
}

// TestDispose_RemoveRefused_TransientStatError_Kept proves the present/gone
// classifier does not misread a transient stat failure (EIO/EACCES) as a gone
// lane: the non-force remove is refused (the lane is still present), the
// <lanePath>/.git stat then fails with a non-ENOENT error, and the lane takes the
// conservative KEEP path — never marked disposed, branch intact — rather than
// being destroyed while the worktree still exists.
func TestDispose_RemoveRefused_TransientStatError_Kept(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	// Late dirty write refuses the non-force remove so the classifier runs.
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}
	// The lane IS still present, but its stat transiently fails (not ENOENT).
	r.s.worktreeLaneStat = func(string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: lanePath, Err: syscall.EIO}
	}

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed || r.disposedEventPresent(t, id) {
		t.Error("transient-stat-error lane wrongly marked disposed")
	}
	if !laneWorktreePresent(lanePath) {
		t.Error("transient-stat-error lane was removed")
	}
	if !r.branchExists(t, id) {
		t.Error("transient-stat-error lane branch was destroyed")
	}
}

// TestDispose_RemoveRefused_RelockFailure_Warns is spec test 13 (re-lock failure):
// the late-dirty lane cannot be re-locked (already locked by another owner mid
// race); disposal warns naming the lane and still keeps it.
func TestDispose_RemoveRefused_RelockFailure_Warns(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	r.s.worktreeDisposeBeforeRemove = func(p string) {
		// Late dirty write refuses the non-force remove, and a foreign lock lands
		// so our re-lock attempt fails.
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
		foreign := worktree.FormatDelegateMarker("dlg_other", "other-session")
		wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreign, p)
	}

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed || r.disposedEventPresent(t, id) {
		t.Fatal("re-lock-failure lane wrongly marked disposed")
	}
	if !laneWorktreePresent(lanePath) {
		t.Error("re-lock-failure lane was removed")
	}
	if !anyContainsAll(warningMessages(r.s), "lock could not be re-acquired", id) {
		t.Error("no warning naming the lane whose re-lock failed")
	}
}

// TestDispose_HalfRemoved_EvictsAndCleans proves half-removed execution: with no
// worktree to remove, dispose marks disposed and deletes the leftover branch +
// sidecar.
func TestDispose_HalfRemoved_MarksAndDeletesBranch(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", lanePath)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.AlreadyDisposed {
		t.Fatal("half-removed disposal reported already-disposed")
	}
	if !r.disposedEventPresent(t, id) {
		t.Error("half-removed lane not marked disposed")
	}
	if r.branchExists(t, id) {
		t.Error("half-removed leftover branch not deleted")
	}
	if _, err := worktree.ReadSidecar(metaDir, id); err == nil {
		t.Error("half-removed leftover sidecar not deleted")
	}
}

// TestDispose_PostGateRefusal_ClearsGate is the clearing half of spec test 10: a
// refusal after the gate is armed (here a step-6 unmerged refusal) reverses the
// gate via the deferred clear-unless-consumed, and the child is NOT evicted.
func TestDispose_PostGateRefusal_ClearsGate(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	child := r.trackRetainedIsolationChild(t, id, lanePath)
	laneCommit(t, lanePath) // unmerged → step-6 refuses (no force)

	sub := r.s.subagents.get(id)
	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "unmerged commit")

	sub.mu.Lock()
	gated := sub.disposeGated
	sub.mu.Unlock()
	if gated {
		t.Error("post-gate refusal left the dispose gate armed")
	}
	if r.s.subagents.get(id) == nil {
		t.Error("post-gate refusal evicted the child (must not)")
	}
	if child.State() == SessionClosed {
		t.Error("post-gate refusal closed the child (must not)")
	}
	if !laneWorktreePresent(lanePath) {
		t.Error("post-gate refusal removed the lane")
	}
}

// TestDispose_KeptAfterEviction_GateConsumed proves the gate is consumed by
// eviction and NOT reversed on a step-8 late-dirty KEEP (spec §P1 step 4/8): the
// child is evicted (removed from the table) even though the lane is downgraded to
// KEPT-after-eviction, and the lane stays resumable (no disposed mark).
func TestDispose_KeptAfterEviction_GateConsumed(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	child := r.trackRetainedIsolationChild(t, id, lanePath)
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	// Child evicted despite the KEEP downgrade — the gate was consumed, not reversed.
	if r.s.subagents.get(id) != nil {
		t.Error("KEPT-after-eviction left the child in the subagent table")
	}
	if child.State() != SessionClosed {
		t.Error("KEPT-after-eviction did not close the evicted child")
	}
	// Lane preserved and resumable.
	if !laneWorktreePresent(lanePath) {
		t.Error("KEPT-after-eviction removed the lane")
	}
	if r.disposedEventPresent(t, id) {
		t.Error("KEPT-after-eviction wrongly marked disposed")
	}
}

// TestDispose_Depth2Cascade_SharedBudget is spec test 12a: disposing a coordinator
// child runs the child's OWN close-time lane disposal — its unchanged grandchild
// lane is collected, its unmerged grandchild lane is KEPT — and the whole
// depth-2 cascade consumes ONE shared budget (no descendant mints a fresh one).
func TestDispose_Depth2Cascade_SharedBudget(t *testing.T) {
	// NOT parallel: it asserts on the process-global closeBudgetMintHook, which is
	// only safe when no other test runs concurrently.
	r := newWorktreeRepo(t)
	coordID, coordLanePath, _ := r.seedIsolationLane(t)

	// The coordinator child is a real session rooted at its lane, with its own
	// state dir so its grandchild lanes live in an isolated worktree root. Reach
	// that state dir through a symlink to ensure Git's canonical porcelain path
	// still matches the path Serf recorded for the lane.
	childStateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(t.TempDir(), childStateDir); err != nil {
		t.Fatalf("symlink child state dir: %v", err)
	}
	childCfg := SessionConfig{StateDir: childStateDir, MaxSubagentDepth: 2, NoProjectPrompts: true}
	child := newSession(t, withDir(coordLanePath), withConfig(childCfg), withoutGitSnapshot())
	r.s.subagents.track(&subagent{id: coordID, sess: child, ownsEnv: true, done: make(chan struct{})})

	// gcClean is unchanged (tip == base) → collectible at the child's close.
	gcCleanID, gcCleanPath := seedIsolationLaneOn(t, child)
	// gcDirty has a commit → unmerged → KEPT at the child's close.
	gcDirtyID, gcDirtyPath := seedIsolationLaneOn(t, child)
	laneCommit(t, gcDirtyPath)

	var mints int32
	closeBudgetMintHook = func() { atomic.AddInt32(&mints, 1) }
	_, err := r.s.worktreeDispose(context.Background(), coordID, false, false)
	closeBudgetMintHook = nil
	if err != nil {
		t.Fatalf("dispose coordinator: %v", err)
	}

	// Exactly one budget minted for the whole depth-2 cascade (dispose owner mints;
	// child close + child lane disposal reuse the inherited deadline).
	if mints != 1 {
		t.Errorf("cascade minted %d budgets, want exactly 1 shared budget", mints)
	}

	// Coordinator lane disposed.
	if laneWorktreePresent(coordLanePath) {
		t.Error("coordinator lane not removed")
	}
	if !r.disposedEventPresent(t, coordID) {
		t.Error("coordinator not marked disposed")
	}
	// Grandchild cascade: clean collected, unmerged KEPT.
	if laneWorktreePresent(gcCleanPath) {
		t.Errorf("unchanged grandchild lane %s not collected by the child's close", gcCleanID)
	}
	if !laneWorktreePresent(gcDirtyPath) {
		t.Errorf("unmerged grandchild lane %s wrongly collected (should be KEPT)", gcDirtyID)
	}
}
