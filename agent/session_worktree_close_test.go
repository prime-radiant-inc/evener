package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for close-time isolation-lane disposal,
// own-worktree close-unlock, and the two revival defenses (native worktree
// tools spec §9 steps 4-6, §5 close-unlock). They build on the wtRepo harness
// from session_tools_worktree_create_test.go.

// seedIsolationLane creates a real isolation delegate lane on disk (the
// parent-side create plumbing, locked with the serf:dlg: marker + sidecar) and
// records the delegate job (started + terminal) so the lane is enumerated as
// one THIS session created. Returns the delegate id, lane path, and the base
// SHA recorded in the sidecar.
func (r *wtRepo) seedIsolationLane(t *testing.T) (delegateID, lanePath, baseSHA string) {
	t.Helper()
	delegateID = jobstore.NewDelegateID()
	path, _, base, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	jobID := jobstore.NewJobID()
	now := time.Now().UTC()
	ref := encodeRef("", "child-"+delegateID)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:          1,
		ChildSessionID:   "child-" + delegateID,
		TranscriptRef:    ref,
		ParentSessionID:  r.s.ID(),
		ParentJobID:      jobID,
		OwnerSessionID:   r.s.ID(),
		VisibleSessionID: r.s.ID(),
		WorkingDir:       path,
		LocalEnvPolicy:   "default",
		Isolation:        "worktree",
	}
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       delegateID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   r.s.ID(),
		VisibleToSession: r.s.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append delegate start: %v", err)
	}
	if err := r.s.jobManager.appendEvent(jobstore.Event{
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
	return delegateID, path, base
}

// laneLocked reports the porcelain lock state of the lane at path.
func (r *wtRepo) laneLocked(t *testing.T, path string) (registered, locked bool, reason string) {
	t.Helper()
	out := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	want := filepath.Clean(path)
	for _, e := range worktree.ParsePorcelain(out) {
		if filepath.Clean(e.Path) == want {
			return true, e.Locked, e.LockReason
		}
	}
	return false, false, ""
}

func (r *wtRepo) branchExists(t *testing.T, name string) bool {
	t.Helper()
	out := wtGit(t, r.mainRoot, "branch", "--list", name)
	return len(out) > 0
}

func (r *wtRepo) disposedEventPresent(t *testing.T, delegateID string) bool {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(r.s.jobManager.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	for _, rec := range recs {
		if rec.DelegateID == delegateID && rec.Disposed {
			return true
		}
	}
	return false
}

// --- Step 4: disposal ---

// TestDisposeUnchangedLane_RemovedAndMarked: an unchanged lane at close is
// removed (worktree + branch + sidecar + lock all gone) and the descriptor is
// marked disposed.
func TestDisposeUnchangedLane_RemovedAndMarked(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(r.canonicalMain(t))

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); !os.IsNotExist(err) {
		t.Errorf("lane worktree still present after disposal: err=%v", err)
	}
	if reg, _, _ := r.laneLocked(t, lanePath); reg {
		t.Error("lane still registered in git after disposal")
	}
	if r.branchExists(t, delegateID) {
		t.Error("lane branch still exists after disposal")
	}
	if _, err := worktree.ReadSidecar(metaDir, delegateID); err == nil {
		t.Error("sidecar still present after disposal")
	}
	// Reload and confirm the disposed mark folds onto the delegate's records.
	recs, err := r.s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, rec := range recs {
		if rec.DelegateID == delegateID {
			found = true
			if !rec.Disposed {
				t.Error("delegate record not marked Disposed after removal")
			}
		}
	}
	if !found {
		t.Fatal("no job record for the disposed delegate")
	}
}

// TestDisposeChangedLane_UnlockedKept: a lane with commits beyond base is
// unlocked, kept, and left resumable (descriptor untouched, no disposed mark);
// the close output lists it, with the real commits-ahead count (not a
// line-count over `rev-list --count`'s single-line integer output, which
// always yields 1 for any positive count).
func TestDisposeChangedLane_UnlockedKept(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	// Add TWO commits in the lane so it is CHANGED, and so the ahead count is
	// distinguishable from the line-count-based bug (which reports 1 for any
	// positive count of commits).
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("progress\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work 1")
	if err := os.WriteFile(filepath.Join(lanePath, "work2.txt"), []byte("progress2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work2.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work 2")

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("changed lane wrongly removed: %v", err)
	}
	reg, locked, _ := r.laneLocked(t, lanePath)
	if !reg {
		t.Fatal("changed lane deregistered")
	}
	if locked {
		t.Error("changed lane still locked; close must unlock a kept lane")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("changed lane branch deleted; must stay resumable")
	}
	// Not disposed: still resumable.
	recs, _ := r.s.jobManager.store.Load()
	for _, rec := range recs {
		if rec.DelegateID == delegateID && rec.Disposed {
			t.Error("changed lane wrongly marked disposed")
		}
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, delegateID, "kept") {
		t.Error("close output did not list the kept changed lane")
	}
	if !anyContainsAll(msgs, "2 ahead") {
		t.Errorf("close output did not report the real commits-ahead count (want 2 ahead): %v", msgs)
	}
}

// TestDisposeDirtyLane_Kept: a lane killed mid-job with uncommitted changes is
// dirty → changed → kept.
func TestDisposeDirtyLane_Kept(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	// Uncommitted (dirty) change only — no commits beyond base.
	if err := os.WriteFile(filepath.Join(lanePath, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("dirty lane wrongly removed: %v", err)
	}
	if _, locked, _ := r.laneLocked(t, lanePath); locked {
		t.Error("dirty kept lane still locked")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("dirty lane branch deleted; must stay resumable")
	}
}

// TestDisposeRacingDirtyWrite_DowngradesToKeep: the non-force remove refuses
// because a write raced the clean check → downgrade to keep + re-lock.
func TestDisposeRacingDirtyWrite_DowngradesToKeep(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	// Seam: dirty the lane immediately before the non-force remove so git
	// refuses it, exercising the downgrade path.
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("lane removed despite racing dirty write: %v", err)
	}
	reg, locked, reason := r.laneLocked(t, lanePath)
	if !reg {
		t.Fatal("lane deregistered after downgrade")
	}
	if !locked {
		t.Error("downgraded lane not re-locked")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != delegateID {
		t.Errorf("re-lock reason = %q, want serf:dlg marker for %s", reason, delegateID)
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("downgraded lane wrongly marked disposed")
	}
}

// TestClose_UnlocksOwnManagedWorktree: a clean close unlocks the session's own
// occupied managed worktree on disk (spec §5 close-unlock).
func TestClose_UnlocksOwnManagedWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "mylane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Fatal("own worktree not locked after create")
	}

	r.s.unlockOwnManagedWorktreeAtClose()

	if _, locked, _ := r.laneLocked(t, path); locked {
		t.Error("own managed worktree still locked after close-unlock")
	}
}

// TestClose_DisposalRunsBeforeStoreClose: a full Close disposes an unchanged
// lane and the disposed mark is durably present in the store afterward, proving
// disposal ran while the store was still open (before closeStoreOnly).
func TestClose_DisposalRunsBeforeStoreClose(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)

	r.s.Close()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); !os.IsNotExist(err) {
		t.Errorf("lane not removed by Close: %v", err)
	}
	if !r.disposedEventPresent(t, delegateID) {
		t.Error("disposed event not durably present after Close; disposal must run before store close")
	}
}

// --- Step 5: revival defenses ---

// TestResumability_RefusesDisposedDelegate: the disposed flag makes
// assessDelegateResumability refuse, and delegate_send surfaces a clear message.
func TestResumability_RefusesDisposedDelegate(t *testing.T) {
	s := newDelegateRestorePreflightSession(t, nil)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	// Sanity: resumable before disposal.
	if a := s.assessDelegateResumability(loadShellRecord(t, s.jobManager, rec.JobID), delegateResumabilityProjection); !a.Resumable {
		t.Fatalf("delegate not resumable before disposal: %s", a.Reason)
	}
	// Mark the delegate disposed.
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         time.Now().UTC(),
		DelegateID: rec.DelegateID,
	}); err != nil {
		t.Fatalf("append disposed: %v", err)
	}
	disposedRec := loadShellRecord(t, s.jobManager, rec.JobID)
	a := s.assessDelegateResumability(disposedRec, delegateResumabilityProjection)
	if a.Resumable || a.Reason != notResumableWorktreeDisposed {
		t.Fatalf("assessment = %+v, want not-resumable with %s", a, notResumableWorktreeDisposed)
	}
	// The delegate_send message is clear and actionable.
	err := notResumableSendError(a.Reason)
	if err == nil || !containsAll(err.Error(), "disposed at session close", "start a new delegate") {
		t.Errorf("send error = %v, want a clear disposed message", err)
	}
}

// TestResumability_RefusesMissingWorkingDir: the unconditional WorkingDir stat
// (crash net) refuses restoration into a deleted directory, covering the crash
// window between remove and mark.
func TestResumability_RefusesMissingWorkingDir(t *testing.T) {
	s := newDelegateRestorePreflightSession(t, nil)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	reloaded := loadShellRecord(t, s.jobManager, rec.JobID)
	if a := s.assessDelegateResumability(reloaded, delegateResumabilityProjection); !a.Resumable {
		t.Fatalf("delegate not resumable before dir removal: %s", a.Reason)
	}
	// Simulate a crash between `git worktree remove` and the disposed mark:
	// the working directory is gone but the descriptor is still live.
	if err := os.RemoveAll(reloaded.DelegateRestore.WorkingDir); err != nil {
		t.Fatalf("remove working dir: %v", err)
	}
	a := s.assessDelegateResumability(reloaded, delegateResumabilityProjection)
	if a.Resumable || a.Reason != notResumableWorkingDirMissing {
		t.Fatalf("assessment = %+v, want not-resumable with %s", a, notResumableWorkingDirMissing)
	}
}
