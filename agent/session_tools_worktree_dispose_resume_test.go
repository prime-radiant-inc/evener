package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// WS8 Task 1 (docs/superpowers/plans/2026-08-06-ws8-worktree-lifecycle.md): a
// session that has been RESUMED must still be able to dispose its own idle
// lane. On restore, armPendingTerminalNotifications re-arms every owned
// terminal record that was never delivered before the session exited, which
// moves the delegate's record to NotifyPending — and the dispose op used to
// read that as "still has running or undelivered work" and refuse the session's
// own lane.
//
// These are real-git integration tests: the subject is a real lane surviving a
// real close, being read back by a session restored from the same ledger, and
// really torn down. They build on the wtRepo harness and
// seedRetainedIsolationLane from session_tools_worktree_remove_force_dispose_test.go.

// newResumableWorktreeRepo is newWorktreeRepo with ONE state dir shared by the
// session's job store and its managed worktree root. The plain constructor
// re-points s.stateDir AFTER the job manager is built, so the live session's
// ledger lands somewhere a session restored against r.stateDir would never read
// — every resume assertion below would then be testing an empty store.
func newResumableWorktreeRepo(t *testing.T) *wtRepo {
	t.Helper()
	stateDir, err := filepath.EvalSymlinks(packageFixtureTempDir(t, "worktree-state-*"))
	if err != nil {
		t.Fatalf("EvalSymlinks state: %v", err)
	}
	cfg := worktreeTestSessionConfig()
	cfg.StateDir = stateDir
	r := newWorktreeRepoWithConfig(t, cfg)
	r.stateDir = stateDir
	r.s.stateDir = stateDir
	return r
}

// resumeSameSession closes r's live session and restores it from its own
// SessionMeta — the real resume path, which PRESERVES meta.ID, so the restored
// session owns exactly the lanes the closed one created. r.s is repointed at the
// restored session so the wtRepo op helpers drive it.
func (r *wtRepo) resumeSameSession(t *testing.T, launchDir string) *Session {
	t.Helper()
	meta := r.s.Meta()
	r.s.Close()
	restored := r.restoreWorktreeSession(t, meta, launchDir)
	if restored.ID() != meta.ID {
		t.Fatalf("resumed session id = %s, want the preserved %s", restored.ID(), meta.ID)
	}
	r.s = restored
	return restored
}

// requireRearmedPending asserts the restore really did re-arm the delegate's
// terminal record to NotifyPending. Without this the tests below could pass on
// a session that never reached the state under test.
func requireRearmedPending(t *testing.T, s *Session, delegateID string) {
	t.Helper()
	recs, err := s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	for _, rec := range recs {
		if rec.DelegateID == delegateID && rec.Type == jobstore.JobDelegate {
			if rec.NotifyState != jobstore.NotifyPending {
				t.Fatalf("resumed record for %s has NotifyState %q, want %q (the re-arm under test did not happen)", delegateID, rec.NotifyState, jobstore.NotifyPending)
			}
			return
		}
	}
	t.Fatalf("no delegate record for %s in the resumed session's store", delegateID)
}

// TestDispose_ResumedSession_DisposesOwnQuiescedLane is the WS8 Task 1 Step-2
// test: a lane whose delegate is genuinely idle is disposable by the session
// that created it, after that session has been resumed. The re-armed terminal
// notification is a render this session owes ITSELF, not work in flight.
func TestDispose_ResumedSession_DisposesOwnQuiescedLane(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
	delegateID, lanePath := seedRetainedIsolationLane(t, r, "")
	// Unmerged work makes the close-time sweep KEEP the lane, so there is still
	// a lane for the resumed session to dispose.
	laneCommit(t, lanePath)

	restored := r.resumeSameSession(t, r.mainRoot)
	requireRearmedPending(t, restored, delegateID)

	requireDisposed(t, restored, r, delegateID, lanePath, true, false)
}

// TestDispose_ResumedSession_TerminalNotificationStillDelivered pins WHY the
// re-armed notification is safe to look past: disposing the lane destroys
// neither the job record nor the queued notification, so the resumed session
// still renders its delegate's terminal result on its next notification turn.
// The gate was protecting a render that disposal never threatened.
func TestDispose_ResumedSession_TerminalNotificationStillDelivered(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
	delegateID, lanePath := seedRetainedIsolationLane(t, r, "")
	laneCommit(t, lanePath)

	restored := r.resumeSameSession(t, r.mainRoot)
	requireRearmedPending(t, restored, delegateID)
	if _, err := restored.worktreeDispose(context.Background(), delegateID, true, false); err != nil {
		t.Fatalf("dispose after resume: %v", err)
	}

	if !restored.acceptNotificationInput(context.Background()) {
		t.Fatal("the re-armed terminal notification was swallowed by the disposal; the resumed session never got its notification turn")
	}
	recs, err := restored.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	for _, rec := range recs {
		if rec.DelegateID == delegateID && rec.Type == jobstore.JobDelegate && rec.NotifyState != jobstore.NotifyDelivered {
			t.Fatalf("record for %s has NotifyState %q after its notification turn, want %q", delegateID, rec.NotifyState, jobstore.NotifyDelivered)
		}
	}
}

// TestDispose_ResumedSession_RunningJobStillRefuses is the first Step-3
// negative: running jobs block removal ALWAYS
// (docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md). Resume
// does not buy a lane with real work in flight.
func TestDispose_ResumedSession_RunningJobStillRefuses(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
	delegateID, lanePath := seedRetainedIsolationLane(t, r, "")
	laneCommit(t, lanePath)

	restored := r.resumeSameSession(t, r.mainRoot)
	requireRearmedPending(t, restored, delegateID)

	restored.jobManager.mu.Lock()
	restored.jobManager.running["job_running"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "job_running", Type: jobstore.JobDelegate, DelegateID: delegateID,
	}}
	restored.jobManager.mu.Unlock()
	defer deleteRunning(restored, "job_running")

	err := disposeErr(t, restored, delegateID, true, false)
	requireRefusalContains(t, err, "running or unfinished work")
	if !laneWorktreePresent(lanePath) {
		t.Fatalf("lane %s torn down despite a running job", lanePath)
	}
}

// TestDispose_ResumedSession_DescendantLaneStillRefused is the second Step-3
// negative: a descendant's forwarded lane descriptor names the DESCENDANT as
// creator, and resume must not re-stamp it onto the resuming ancestor. The
// record's ownership scoping ("defaults to not destroying other sessions'
// work") holds across a resume, so an ancestor still cannot dispose a live
// descendant's lane.
func TestDispose_ResumedSession_DescendantLaneStillRefused(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)

	// A forwarded descendant copy: the descriptor's ParentSessionID is the
	// descendant that created the lane, not this session.
	descendantID := jobstore.NewDelegateID()
	jobID := jobstore.NewJobID(r.s.ID())
	now := time.Now().UTC()
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: jobID, DelegateID: descendantID,
		Type: jobstore.JobDelegate, StartedAt: &now,
		OwnerSessionID: "descendant-session", VisibleToSession: r.s.ID(),
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{
			Version:         1,
			ParentSessionID: "descendant-session",
			OwnerSessionID:  "descendant-session",
			WorkingDir:      filepath.Join(r.mainRoot, "descendant-lane"),
			Isolation:       "worktree",
		},
	}); err != nil {
		t.Fatalf("append forwarded descendant record: %v", err)
	}

	restored := r.resumeSameSession(t, r.mainRoot)

	err := disposeErr(t, restored, descendantID, true, false)
	requireRefusalContains(t, err, "created by another session")
}

// TestWorktreeRemove_Force_ResumedSession_CascadeDisposesAndRemoves is Step 4:
// the Task-2 force cascade now runs end to end in a RESUMED session. Before the
// quiescence fix, step 4 found the lane eligible (ownership passes on resume),
// steps 5-7 ran, and only then did the cascaded dispose refuse on the re-armed
// notification — a refusal that had already swapped the session's env.
func TestWorktreeRemove_Force_ResumedSession_CascadeDisposesAndRemoves(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
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
	// Commit so the close-time sweep keeps the lane, then fast-forward main onto
	// it so the cascaded dispose — which always runs force:false — judges it
	// collectible on its own D0-model terms.
	laneCommit(t, lanePath)

	restored := r.resumeSameSession(t, r.mainRoot)
	requireRearmedPending(t, restored, delegateID)
	wtGit(t, r.mainRoot, "merge", "--ff-only", delegateID)
	trackRetainedChild(t, restored, delegateID, nested)

	out, err := r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err != nil {
		t.Fatalf("remove force:true in a resumed session: %v", err)
	}
	if out["path"] != path {
		t.Errorf("removed path = %v, want %s", out["path"], path)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("worktree %s still present after force remove", path)
	}
	if laneWorktreePresent(lanePath) {
		t.Errorf("delegate lane %s not disposed by the resumed cascade", lanePath)
	}
	if r.branchExists(t, delegateID) {
		t.Errorf("delegate branch %s not deleted by the resumed cascade", delegateID)
	}
}

// TestWorktreeRemove_Force_ResumedSession_UnmergedCascadeStillRefuses pins that
// the fix did not widen the cascade: a lane with genuinely unmerged work still
// refuses on dispose's own D0-model terms after a resume, and the refusing call
// destroys nothing — neither the delegate's lane and branch nor the target.
func TestWorktreeRemove_Force_ResumedSession_UnmergedCascadeStillRefuses(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
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

	restored := r.resumeSameSession(t, r.mainRoot)
	requireRearmedPending(t, restored, delegateID)
	trackRetainedChild(t, restored, delegateID, nested)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected the cascaded dispose's own unmerged-work gate to refuse")
	}
	requireRefusalContains(t, err, "unmerged commit")
	if !laneWorktreePresent(lanePath) {
		t.Errorf("delegate lane %s destroyed by a refusing call", lanePath)
	}
	if !r.branchExists(t, delegateID) {
		t.Errorf("delegate branch %s deleted by a refusing call", delegateID)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("target %s removed despite the cascade refusal: %v", path, statErr)
	}
}

// trackRetainedChild attaches a retained-idle child session rooted at dir for
// delegateID, the shape a resumed coordinator is in once it has revived its
// delegate: liveWorkUnder's physical scan reports the child, and
// retainedIdleDelegateIDs resolves it back to the lane the cascade disposes.
// The child carries the session id the ledger recorded for this delegate, as a
// real revival does — that id is the only key linking the physical scan's
// blocker back to the delegate record.
func trackRetainedChild(t *testing.T, s *Session, delegateID, dir string) {
	t.Helper()
	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("load delegates: %v", err)
	}
	childID := ""
	for _, d := range delegates {
		if d != nil && d.DelegateID == delegateID {
			childID = d.ChildSessionID
		}
	}
	if childID == "" {
		t.Fatalf("no recorded child session id for delegate %s", delegateID)
	}
	child := newSession(t, withDir(dir), withoutGitSnapshot())
	child.id = childID
	s.subagents.track(&subagent{id: delegateID, sess: child, done: make(chan struct{})})
}

// TestWorktreeRemove_Force_ResumedSession_RefusingCascadeInsideTargetDestroysNothing
// completes Step 4 for the shape where remove's step 7 really does mutate: the
// resumed session is INSIDE the target, so a refusal reached after step 7 has
// already swapped its env and released its lock.
//
// What must hold, and does: the refusal is the cascaded dispose's REAL gate (a
// lane with unmerged work), not the re-armed-notification gate this task
// removed, and the call destroys nothing — the target, the delegate's lane, and
// its branch are all still there.
//
// What is deliberately NOT claimed: that the refusal happens before step 7. It
// does not, and that is I1's accepted, recoverable cost (a destructive cascade
// must not run until remove's own gates have passed). Asserted here rather than
// left implicit so that any future reordering has to face it: the env swap and
// unlock are exactly what a caller retrying after merging the lane would have to
// redo.
func TestWorktreeRemove_Force_ResumedSession_RefusingCascadeInsideTargetDestroysNothing(t *testing.T) {
	t.Parallel()
	r := newResumableWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	nested := filepath.Join(path, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	delegateID, lanePath := seedRetainedIsolationLane(t, r, nested)
	laneCommit(t, lanePath)

	// Resume INSIDE the target, the shape that makes step 7 mutate.
	restored := r.resumeSameSession(t, path)
	if got := restored.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("resumed env = %q, want the target %q (the fixture never reached the currently-inside shape)", got, path)
	}
	trackRetainedChild(t, restored, delegateID, nested)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected the cascaded dispose's own unmerged-work gate to refuse")
	}
	requireRefusalContains(t, err, "unmerged commit")
	if !r.lanePresent(path) {
		t.Error("target removed by a refusing call")
	}
	if !laneWorktreePresent(lanePath) {
		t.Error("delegate lane destroyed by a refusing call")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("delegate branch deleted by a refusing call")
	}
	if got := restored.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("session env = %q, want the restore root %q — remove's step 7 runs before the cascade (I1 ordering)", got, r.mainRoot)
	}
	if _, locked, reason := r.laneLocked(t, path); locked {
		t.Errorf("target still locked (%q) — step 7's unlock runs before the cascade (I1 ordering)", reason)
	}
}
