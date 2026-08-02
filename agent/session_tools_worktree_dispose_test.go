package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These are unit tests for the model-facing dispose operation's validation
// ladder (spec §P1 steps 1-6) and its execution (steps 7-8). Every refusal rung
// returns its precise error; a collectible lane is evicted, removed, marked
// disposed, and its branch + sidecar deleted.
//
// This file is MIXED across the two lane harnesses; see docs/testing.md for the
// rule. The refusal rungs are decisions serf makes from its own records, gates
// and lock markers, so they run on the scripted git boundary (scriptedLaneRepo).
// The tests below stay on real git (wtRepo) because their subject IS git's own
// behavior — real dirty detection, real divergent ancestry, real removal, and the
// real ref store a surviving branch's tip is resolved against:
//
//   - TestDispose_UnchangedLane_Disposed (real removal + real branch deletion)
//   - TestDispose_UnmergedRefusedForceOverrides
//   - TestDispose_DirtyRefusedForceDirtyOverrides
//   - TestDispose_HalfRemoved_MergedDisposed_UnmergedRefused
//   - TestDispose_HalfRemovedUnchanged_Disposed
//   - TestDispose_AlreadyDisposed_IdempotentCleanup

func disposeErr(t *testing.T, s *Session, id string, force, forceDirty bool) error {
	t.Helper()
	_, err := s.worktreeDispose(context.Background(), id, force, forceDirty)
	return err
}

// laneStateProbe reports the harness-specific lane facts the dispose assertions
// check, so one set of helpers serves both the real-git wtRepo and the scripted
// scriptedLaneRepo.
type laneStateProbe interface {
	branchExists(t *testing.T, name string) bool
	disposedEventPresent(t *testing.T, delegateID string) bool
}

// requireDisposed asserts the dispose ran to completion: no error, the lane
// directory removed, the disposed mark durably present, and the branch deleted.
func requireDisposed(t *testing.T, s *Session, probe laneStateProbe, id, lanePath string, force, forceDirty bool) WorktreeDisposeResult {
	t.Helper()
	res, err := s.worktreeDispose(context.Background(), id, force, forceDirty)
	if err != nil {
		t.Fatalf("expected disposal, got error: %v", err)
	}
	if laneWorktreePresent(lanePath) {
		t.Fatalf("lane %s not removed after dispose", lanePath)
	}
	if !probe.disposedEventPresent(t, id) {
		t.Fatalf("disposed mark not present for %s", id)
	}
	if probe.branchExists(t, id) {
		t.Fatalf("branch %s not deleted after dispose", id)
	}
	return res
}

func requireRefusalContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected refusal containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal %q does not contain %q", err.Error(), want)
	}
}

func TestDispose_UnknownID_InvalidRequest(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	err := disposeErr(t, r.s, "dlg_doesnotexist", false, false)
	requireRefusalContains(t, err, "invalid_request")
	requireRefusalContains(t, err, "not a known isolation delegate")
}

func TestDispose_NonDelegateID_InvalidRequest(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	err := disposeErr(t, r.s, "some-worktree-name", false, false)
	requireRefusalContains(t, err, "invalid_request")
	requireRefusalContains(t, err, "not a delegate id")
}

func TestDispose_EmptyID_InvalidRequest(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	err := disposeErr(t, r.s, "   ", false, false)
	requireRefusalContains(t, err, "invalid_request")
}

func TestDispose_ForwardedRecord_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	// Append a worktree-isolation delegate record owned by ANOTHER session (a
	// forwarded descendant copy). No disk lane is needed: ownership is checked
	// before any lane inspection.
	id := jobstore.NewDelegateID()
	jobID := jobstore.NewJobID(r.s.ID())
	now := time.Now().UTC()
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:         1,
		ParentSessionID: "some-other-session",
		WorkingDir:      filepath.Join(r.mainRoot, "nope"),
		Isolation:       "worktree",
	}
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: jobID, DelegateID: id,
		Type: jobstore.JobDelegate, StartedAt: &now, DelegateRestore: desc,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "created by another session")
}

func TestDispose_RunningJob_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, _ := r.seedIsolationLane(t)

	// Inject a running delegate job for this id so record quiescence fails.
	r.s.jobManager.mu.Lock()
	r.s.jobManager.running["job_running"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "job_running", Type: jobstore.JobDelegate, DelegateID: id,
	}}
	r.s.jobManager.mu.Unlock()
	defer deleteRunning(r.s, "job_running")

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "running or undelivered work")
}

func TestDispose_ArmedWatchSendTo_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, _ := r.seedIsolationLane(t)

	r.s.jobManager.mu.Lock()
	r.s.jobManager.watches[watchKey{Target: "job_w", SendTo: id}] = &watchConfig{
		send: &watchSendArgs{To: id},
	}
	r.s.jobManager.mu.Unlock()

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "armed or pending watch send")
}

func TestDispose_PendingWatchSend_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, _ := r.seedIsolationLane(t)

	key := jobstore.WatchSendKey{VisibleSessionID: r.s.ID(), WatchTarget: "job_w", ResolvedSendTo: id}
	r.s.jobManager.mu.Lock()
	r.s.jobManager.watches[watchKey{Target: "job_w", SendTo: id}] = &watchConfig{
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: {Key: key}},
	}
	r.s.jobManager.mu.Unlock()

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "armed or pending watch send")
}

func TestDispose_LiveShellUnderLane_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, lanePath := r.seedIsolationLane(t)

	r.s.jobManager.mu.Lock()
	r.s.jobManager.running["job_shell"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "job_shell", Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		WorkingDir: filepath.Join(lanePath, "sub"),
	}}
	r.s.jobManager.mu.Unlock()
	defer deleteRunning(r.s, "job_shell")

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "live background shell")
}

func TestDispose_ForeignLock_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, lanePath := r.seedIsolationLane(t)

	// Replace the own dlg marker with a foreign delegate marker (another
	// session's lane): step 5 must refuse and not remove.
	r.setLaneLock(t, lanePath, worktree.FormatDelegateMarker("dlg_other", "other-session"))

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "locked by another owner")
}

// laneAheadCount's own unresolved-count answer is a plain 0 (kata cn94): when
// the rev-list read that decorates an unmerged refusal fails, the message must
// not claim the lane "has 0 unmerged commit(s)" — that reads as self-refuting
// (an instruction to merge zero commits) rather than as an unknown count.
func TestDispose_UnmergedRefusalWithUnresolvableAheadCountReadsUnknown(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, lanePath := r.seedIsolationLane(t)

	// Move the lane's tip off its base so disposableReason's tip==baseSHA
	// short-circuit (Unchanged, collectible) does not bypass the unmerged
	// branch before laneAheadCount is ever reached.
	entry := r.git.entry(lanePath)
	if entry == nil {
		t.Fatalf("no scripted entry for lane %s", lanePath)
	}
	entry.head = "0000000000000000000000000000000000dead"

	// Blank the sidecar's merge target so disposableReason resolves via
	// worktree.Merged's TargetUnknown short-circuit (still "unmerged" for
	// dispose's purposes) rather than merge-base/cherry, which the scripted
	// model now refuses to answer outright (kata e312).
	if err := worktree.UpdateSidecar(metaDirForLane(lanePath), id, func(sc *worktree.Sidecar) {
		sc.MergeTarget = ""
	}); err != nil {
		t.Fatalf("UpdateSidecar: %v", err)
	}

	// Fail the ahead-count read itself, independent of the merge verdict above.
	r.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
		if len(args) == 5 && args[2] == "rev-list" && args[3] == "--count" {
			return "", errors.New("scripted git: injected rev-list failure")
		}
		return next(args...)
	})

	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "unmerged commit")
	if strings.Contains(err.Error(), "has 0 unmerged commit") {
		t.Errorf("refusal claims a bogus zero count on an unresolvable ahead-count read: %v", err)
	}
}

func TestDispose_UnchangedLane_Disposed(t *testing.T) {
	t.Parallel()
	// REAL git: the happy path's proof is that git really deregisters the
	// worktree and really deletes the branch.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	// A freshly cut lane is Unchanged (tip == base) and clean → collectible.
	res := requireDisposed(t, r.s, r, id, lanePath, false, false)
	if res.AlreadyDisposed {
		t.Fatal("first disposal should not report already-disposed")
	}
	if res.LanePath != lanePath || res.Branch != id {
		t.Fatalf("result = %+v, want lane %s branch %s", res, lanePath, id)
	}
}

func TestDispose_UnmergedRefusedForceOverrides(t *testing.T) {
	t.Parallel()
	// REAL git: real divergent ancestry drives the unmerged verdict, and force
	// must really force-delete a branch carrying commits.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	laneCommit(t, lanePath)

	// clean + unmerged → refused.
	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "unmerged commit")
	// force_dirty alone does NOT override an unmerged refusal.
	requireRefusalContains(t, disposeErr(t, r.s, id, false, true), "unmerged commit")
	// force overrides → disposed (branch -D force-deletes the unmerged commits).
	requireDisposed(t, r.s, r, id, lanePath, true, false)
}

func TestDispose_DirtyRefusedForceDirtyOverrides(t *testing.T) {
	t.Parallel()
	// REAL git: real dirty detection drives the refusal, and force_dirty must
	// really force-remove a dirty worktree.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	mustWriteFile(t, filepath.Join(lanePath, "dirty.txt"), "uncommitted")

	// dirty → refused.
	requireRefusalContains(t, disposeErr(t, r.s, id, false, false), "uncommitted changes")
	// force alone does NOT override a dirty refusal.
	requireRefusalContains(t, disposeErr(t, r.s, id, true, false), "uncommitted changes")
	// force_dirty overrides → the dirty lane is force-removed and disposed.
	requireDisposed(t, r.s, r, id, lanePath, false, true)
}

func TestDispose_HalfRemoved_MergedDisposed_UnmergedRefused(t *testing.T) {
	t.Parallel()
	// REAL git: half-removed registry state plus a real unmerged branch tip.
	// Unmerged half-removed: commit on the branch, then remove only the worktree
	// dir (branch + record + sidecar remain).
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	laneCommit(t, lanePath)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", lanePath)

	if laneWorktreePresent(lanePath) {
		t.Fatal("expected lane dir removed")
	}
	err := disposeErr(t, r.s, id, false, false)
	requireRefusalContains(t, err, "half-removed")
	// force overrides the unmerged half-removed refusal → branch + sidecar deleted.
	requireDisposed(t, r.s, r, id, lanePath, true, false)
}

func TestDispose_HalfRemovedUnchanged_Disposed(t *testing.T) {
	t.Parallel()
	// A half-removed lane whose branch tip == base (unchanged) is collectible.
	// REAL git: with the worktree gone, the verdict comes from resolving the
	// surviving branch ref in the main repo.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", lanePath)
	requireDisposed(t, r.s, r, id, lanePath, false, false)
}

func TestDispose_AlreadyDisposed_IdempotentCleanup(t *testing.T) {
	t.Parallel()
	// REAL git: the remnant branch's own tip decides whether the idempotent
	// cleanup may delete it, resolved against the real ref store.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	// Mark disposed but leave branch + sidecar as remnants (crash between mark
	// and cleanup). A re-issued dispose is a no-op that clears them.
	r.appendDisposed(t, id)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", lanePath)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("already-disposed dispose returned error: %v", err)
	}
	if !res.AlreadyDisposed {
		t.Fatalf("expected AlreadyDisposed, got %+v", res)
	}
	// Unchanged branch tip → collectible → branch deleted.
	if r.branchExists(t, id) {
		t.Fatal("already-disposed cleanup should have deleted the leftover branch")
	}
}

func TestDispose_ReissuedAfterDisposal_CleanAlreadyDisposed(t *testing.T) {
	t.Parallel()
	// A successful dispose deletes the sidecar (step 8). Re-issuing dispose on the
	// same id must reach the idempotent already-disposed short-circuit and report a
	// clean no-op — NOT fail the sidecar read with "sidecar unreadable" (the live
	// eval's product bug #1). RED before the fix: the disposed short-circuit sat
	// after the hard sidecar-read error, so the gone sidecar erred.
	r := newScriptedLaneRepo(t)
	id, lanePath := r.seedIsolationLane(t)

	// First dispose an unchanged lane to completion.
	requireDisposed(t, r.s, r, id, lanePath, false, false)
	metaDir := metaDirForLane(lanePath)
	if _, err := worktree.ReadSidecar(metaDir, id); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar gone after dispose, got err=%v", err)
	}

	// Re-issue dispose: clean already-disposed, no error, no "unreadable".
	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("re-issued dispose returned error: %v", err)
	}
	if !res.AlreadyDisposed {
		t.Fatalf("expected AlreadyDisposed, got %+v", res)
	}
	if strings.Contains(res.Message, "unreadable") {
		t.Fatalf("re-issued dispose message reports unreadable: %q", res.Message)
	}
}

func TestDispose_SessionClosing_Refused(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, _ := r.seedIsolationLane(t)
	r.s.mu.Lock()
	r.s.closing = true
	r.s.mu.Unlock()
	requireRefusalContains(t, disposeErr(t, r.s, id, false, false), "session is closing")
}

// deleteRunning removes a test-injected fake running job so the session's close
// cleanup does not try to finalize an incomplete runningJob.
func deleteRunning(s *Session, jobID string) {
	s.jobManager.mu.Lock()
	delete(s.jobManager.running, jobID)
	s.jobManager.mu.Unlock()
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// laneCommit makes a real commit on the lane's branch so the lane is ahead of
// its base (unmerged). Real git only: divergent ancestry has no scripted stand-in.
func laneCommit(t *testing.T, lanePath string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(lanePath, "work.txt"), "lane work")
	wtGit(t, lanePath, "add", "-A")
	wtGit(t, lanePath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "lane work")
}
