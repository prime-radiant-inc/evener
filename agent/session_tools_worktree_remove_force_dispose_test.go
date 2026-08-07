package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// WS8 Task 2 (docs/superpowers/plans/2026-08-06-ws8-worktree-lifecycle.md):
// `manage_worktree remove force:true` disposes retained-idle delegate lanes
// through the sanctioned dispose path (worktreeDispose — the same op
// `manage_worktree op=dispose` runs), rather than refusing and telling the
// caller to dispose them by hand. These are real-git integration tests (the
// subject is git's own worktree/branch removal, and the dispose cascade must
// really run to completion), so they build on the wtRepo harness from
// session_tools_worktree_create_test.go and session_worktree_close_test.go's
// seedIsolationLane-style real lane creation.
//
// A delegate's lane is always a SIBLING of every other managed worktree under
// the same project directory (spec §6 — worktreeRoot/projectID/name is flat),
// never nested inside another worktree, so a retained delegate blocks
// `remove <other lane>` only by having strayed there: liveWorkUnder's
// containment scan reports where the tracked child session's CURRENT env is,
// while worktreeDispose operates on the lane its job record names. The
// two-directory fixture below is the smallest real-git construction that
// exercises both of the cascade's independent legs. The one-directory shape
// (child in its own lane, that lane named directly to remove) is covered
// separately.

// seedRetainedIsolationLane creates a real, disposable worktree-isolation lane
// for parent (createDelegateWorktree — always a sibling under the managed
// directory, never nested under another worktree per spec §6), and tracks a
// retained-idle child session rooted at childDir, recording the delegate job
// with that child's REAL session id so liveWorkUnder's physical scan and
// retainedIdleDelegateIDs' delegate-id lookup both resolve it to the same lane.
// childDir "" roots the child in its own lane (the ordinary shape); a path
// under some other worktree models a child that strayed there, so the blocker
// liveWorkUnder reports and the lane the cascade disposes are different
// directories.
func seedRetainedIsolationLane(t *testing.T, parent *wtRepo, childDir string) (delegateID, lanePath string) {
	t.Helper()
	delegateID = jobstore.NewDelegateID()
	path, _, _, _, _, err := parent.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	if childDir == "" {
		childDir = path
	}
	child := newSession(t, withDir(childDir), withoutGitSnapshot())
	jobID := jobstore.NewJobID(parent.s.ID())
	now := time.Now().UTC()
	ref := encodeRef("", child.id)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:          1,
		ChildSessionID:   child.id,
		TranscriptRef:    ref,
		ParentSessionID:  parent.s.ID(),
		ParentJobID:      jobID,
		OwnerSessionID:   parent.s.ID(),
		VisibleSessionID: parent.s.ID(),
		WorkingDir:       path,
		LocalEnvPolicy:   "default",
		Isolation:        "worktree",
	}
	// LoadDelegates (which retainedIdleDelegateIDs' ChildSessionID->DelegateID
	// lookup depends on) folds ChildSessionID from EventDelegateCreated, not
	// from the DelegateRestoreDescriptor on EventJobStarted — both must be
	// recorded, matching seedRetainedDelegateWithIsolation's pattern.
	if err := parent.s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   child.id,
			TranscriptRef:    ref,
			OwnerSessionID:   parent.s.ID(),
			VisibleSessionID: parent.s.ID(),
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate created: %v", err)
	}
	if err := parent.s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       delegateID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   parent.s.ID(),
		VisibleToSession: parent.s.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append delegate start: %v", err)
	}
	if err := parent.s.jobManager.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusStopped,
		Reason:      "runtime_lost",
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("append delegate finished: %v", err)
	}
	parent.s.subagents.track(&subagent{id: delegateID, sess: child, done: make(chan struct{})})
	return delegateID, path
}

// TestWorktreeRemove_Force_DisposesRetainedIdleLaneThenRemoves is brief Step
// 1(a): a tree whose only live-work entries are retained-idle delegate lanes
// — `remove force:true` disposes each lane through the sanctioned path, then
// removes the target.
func TestWorktreeRemove_Force_DisposesRetainedIdleLaneThenRemoves(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	delegateID, lanePath := seedRetainedIsolationLane(t, r, nested)

	out, err := r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err != nil {
		t.Fatalf("remove force:true: %v", err)
	}
	if out["path"] != path {
		t.Errorf("removed path = %v, want %s", out["path"], path)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("lane %s still present after force remove", path)
	}
	if _, statErr := os.Stat(lanePath); statErr == nil {
		t.Errorf("delegate lane %s not disposed by the cascade", lanePath)
	}
	if r.branchExists(t, delegateID) {
		t.Errorf("delegate branch %s not deleted by the cascade", delegateID)
	}
}

// TestWorktreeRemove_Force_UnmergedRetainedIdleLaneCascadeRefuses is brief
// Step 1(b): each cascaded dispose keeps its own gates. remove's force does
// NOT invent a new force semantics for dispose — the cascade always calls
// dispose with force:false, forceDirty:false, so an unmerged lane still
// refuses on dispose's own terms and that refusal bubbles up as remove's
// refusal, leaving the target worktree untouched.
func TestWorktreeRemove_Force_UnmergedRetainedIdleLaneCascadeRefuses(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	delegateID, lanePath := seedRetainedIsolationLane(t, r, nested)
	laneCommit(t, lanePath)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the cascaded dispose's own unmerged-work gate")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unmerged commit") {
		t.Errorf("refusal should surface dispose's own unmerged-commit reason, got: %v", msg)
	}
	if !strings.Contains(msg, delegateID) {
		t.Errorf("refusal should name the delegate id %s, got: %v", delegateID, msg)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("lane %s removed despite the cascade refusal: %v", path, statErr)
	}
	if _, statErr := os.Stat(lanePath); statErr != nil {
		t.Errorf("unmerged delegate lane %s removed despite refusing force_dirty/force overrides in the cascade: %v", lanePath, statErr)
	}
}

// TestWorktreeRemove_Force_RunningJobStillRefuses is brief Step 1(c): a
// genuinely running job under the tree still refuses remove force:true,
// naming the job — running jobs block removal always, with no force
// override, so the cascade must never engage for this blocker.
func TestWorktreeRemove_Force_RunningJobStillRefuses(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	env := r.s.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	shellRes := runShell(context.Background(), r.s.jobManager, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
		WorkingDir: env.WorkingDirectory(),
	})
	if shellRes.JobID == "" || !shellRes.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", shellRes)
	}
	t.Cleanup(func() { _, _ = r.s.jobManager.stop(shellRes.JobID) })
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the running job")
	}
	if !strings.Contains(err.Error(), shellRes.JobID) {
		t.Errorf("refusal should name the running job %s, got: %v", shellRes.JobID, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("lane %s removed despite the running job: %v", path, statErr)
	}
}

// TestWorktreeRemove_Force_MixedBlockersRefuseWithoutCascading pins the
// cascade's fail-closed rule: it engages only when EVERY blocker is a
// retained-idle owned lane. One running job alongside an otherwise disposable
// lane refuses the whole remove and disposes nothing — force must never buy a
// partial teardown on a call that goes on to refuse.
func TestWorktreeRemove_Force_MixedBlockersRefuseWithoutCascading(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	env := r.s.currentEnv()
	se, ok := env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("session env does not support streaming")
	}
	shellRes := runShell(context.Background(), r.s.jobManager, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
		WorkingDir: path,
	})
	if shellRes.JobID == "" || !shellRes.RunningInBackground {
		t.Fatalf("shell result = %+v, want a running background job", shellRes)
	}
	t.Cleanup(func() { _, _ = r.s.jobManager.stop(shellRes.JobID) })
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	delegateID, lanePath := seedRetainedIsolationLane(t, r, nested)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the running job blocker")
	}
	if !strings.Contains(err.Error(), shellRes.JobID) {
		t.Errorf("refusal should name the running job %s, got: %v", shellRes.JobID, err)
	}
	if _, statErr := os.Stat(lanePath); statErr != nil {
		t.Errorf("delegate lane %s disposed despite the mixed-blocker refusal: %v", lanePath, statErr)
	}
	if !r.branchExists(t, delegateID) {
		t.Errorf("delegate branch %s deleted despite the mixed-blocker refusal", delegateID)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("lane %s removed despite the running job: %v", path, statErr)
	}
}

// TestWorktreeRemove_Force_LaneNamedDirectly_LockedRefuses pins that force
// still stops at the lock guard when the delegate lane itself is the named
// target. A live lane carries its own serf:dlg:<dlg>:<sid> marker, which
// classifies Foreign to the parent session's remove (a different delegate of
// one's own session is not one's own occupancy), so remove refuses at step 3
// and the cascade never runs — force does not override a lock.
func TestWorktreeRemove_Force_LaneNamedDirectly_LockedRefuses(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath := seedRetainedIsolationLane(t, r, "")

	_, err := r.removeOp(t, map[string]any{"name": delegateID, "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the lane's own delegate lock")
	}
	if !strings.Contains(err.Error(), "force does not override a lock") {
		t.Errorf("refusal should be the lock guard's, got: %v", err)
	}
	if _, statErr := os.Stat(lanePath); statErr != nil {
		t.Errorf("locked lane %s removed despite the lock refusal: %v", lanePath, statErr)
	}
}

// TestWorktreeRemove_Force_LaneNamedDirectly_UnlockedCascades covers the
// cascade's "target removed itself" outcome: an unlocked lane (crash residue —
// the only shape that gets past the lock guard above) named directly to remove
// is disposed through the sanctioned path, which tears down the very worktree
// remove was asked to remove. remove reports the disposal honestly rather than
// re-running a `git worktree remove` on a path that is already gone.
func TestWorktreeRemove_Force_LaneNamedDirectly_UnlockedCascades(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath := seedRetainedIsolationLane(t, r, "")
	r.unlockLane(t, lanePath)

	out, err := r.removeOp(t, map[string]any{"name": delegateID, "force": true})
	if err != nil {
		t.Fatalf("remove force:true: %v", err)
	}
	if out["path"] != lanePath {
		t.Errorf("removed path = %v, want %s", out["path"], lanePath)
	}
	if _, statErr := os.Stat(lanePath); statErr == nil {
		t.Errorf("lane %s still present after the cascade", lanePath)
	}
	if r.branchExists(t, delegateID) {
		t.Errorf("delegate branch %s not deleted by the cascade", delegateID)
	}
	if out["branch_deleted"] != true {
		t.Errorf("branch_deleted = %v, want true (the cascade really deleted %s)", out["branch_deleted"], delegateID)
	}
}
