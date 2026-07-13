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

// These are REAL-git unit tests for the model-facing dispose operation's
// validation ladder (spec §P1 steps 1-6). They build on the wtRepo /
// seedIsolationLane harness. This task ends at evaluation: a clean, collectible
// lane returns errDisposeExecutionNotImplemented (execution lands in the
// follow-up task); every refusal rung returns its precise error.

func disposeErr(t *testing.T, r *wtRepo, id string, force, forceDirty bool) error {
	t.Helper()
	_, err := r.s.worktreeDispose(context.Background(), id, force, forceDirty)
	return err
}

// requireStub asserts the dispose reached a collectible evaluation and returned
// the execution stub — i.e. every refusal rung passed.
func requireStub(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errDisposeExecutionNotImplemented) {
		t.Fatalf("expected execution stub, got: %v", err)
	}
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
	r := newWorktreeRepo(t)
	err := disposeErr(t, r, "dlg_doesnotexist", false, false)
	requireRefusalContains(t, err, "invalid_request")
	requireRefusalContains(t, err, "not a known isolation delegate")
}

func TestDispose_NonDelegateID_InvalidRequest(t *testing.T) {
	r := newWorktreeRepo(t)
	err := disposeErr(t, r, "some-worktree-name", false, false)
	requireRefusalContains(t, err, "invalid_request")
	requireRefusalContains(t, err, "not a delegate id")
}

func TestDispose_EmptyID_InvalidRequest(t *testing.T) {
	r := newWorktreeRepo(t)
	err := disposeErr(t, r, "   ", false, false)
	requireRefusalContains(t, err, "invalid_request")
}

func TestDispose_ForwardedRecord_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	// Append a worktree-isolation delegate record owned by ANOTHER session (a
	// forwarded descendant copy). No disk lane is needed: ownership is checked
	// before any lane inspection.
	id := jobstore.NewDelegateID()
	jobID := jobstore.NewJobID()
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
	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "created by another session")
}

func TestDispose_RunningJob_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedIsolationLane(t)

	// Inject a running delegate job for this id so record quiescence fails.
	r.s.jobManager.mu.Lock()
	r.s.jobManager.running["job_running"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "job_running", Type: jobstore.JobDelegate, DelegateID: id,
	}}
	r.s.jobManager.mu.Unlock()
	defer deleteRunning(r, "job_running")

	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "running or undelivered work")
}

func TestDispose_ArmedWatchSendTo_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedIsolationLane(t)

	r.s.jobManager.mu.Lock()
	r.s.jobManager.watches[watchKey{Target: "job_w", SendTo: id}] = &watchConfig{
		send: &watchSendArgs{To: id},
	}
	r.s.jobManager.mu.Unlock()

	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "armed or pending watch send")
}

func TestDispose_PendingWatchSend_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedIsolationLane(t)

	key := jobstore.WatchSendKey{VisibleSessionID: r.s.ID(), WatchTarget: "job_w", ResolvedSendTo: id}
	r.s.jobManager.mu.Lock()
	r.s.jobManager.watches[watchKey{Target: "job_w", SendTo: id}] = &watchConfig{
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: {Key: key}},
	}
	r.s.jobManager.mu.Unlock()

	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "armed or pending watch send")
}

func TestDispose_LiveShellUnderLane_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	r.s.jobManager.mu.Lock()
	r.s.jobManager.running["job_shell"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "job_shell", Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		WorkingDir: filepath.Join(lanePath, "sub"),
	}}
	r.s.jobManager.mu.Unlock()
	defer deleteRunning(r, "job_shell")

	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "live background shell")
}

func TestDispose_ForeignLock_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)

	// Replace the own dlg marker with a foreign delegate marker (another
	// session's lane): step 5 must refuse and not remove.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	foreign := worktree.FormatDelegateMarker("dlg_other", "other-session")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreign, lanePath)

	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "locked by another owner")
}

func TestDispose_UnchangedLane_ReachesStub(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedIsolationLane(t)
	// A freshly cut lane is Unchanged (tip == base) and clean → collectible.
	requireStub(t, disposeErr(t, r, id, false, false))
}

func TestDispose_UnmergedRefusedForceOverrides(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	laneCommit(t, lanePath)

	// clean + unmerged → refused.
	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "unmerged commit")
	// force_dirty alone does NOT override an unmerged refusal.
	requireRefusalContains(t, disposeErr(t, r, id, false, true), "unmerged commit")
	// force overrides → reaches the stub.
	requireStub(t, disposeErr(t, r, id, true, false))
}

func TestDispose_DirtyRefusedForceDirtyOverrides(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	mustWriteFile(t, filepath.Join(lanePath, "dirty.txt"), "uncommitted")

	// dirty → refused.
	requireRefusalContains(t, disposeErr(t, r, id, false, false), "uncommitted changes")
	// force alone does NOT override a dirty refusal.
	requireRefusalContains(t, disposeErr(t, r, id, true, false), "uncommitted changes")
	// force_dirty overrides. The dirty (unchanged-otherwise) lane is then
	// collectible → stub.
	requireStub(t, disposeErr(t, r, id, false, true))
}

func TestDispose_HalfRemoved_MergedStub_UnmergedRefused(t *testing.T) {
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
	err := disposeErr(t, r, id, false, false)
	requireRefusalContains(t, err, "half-removed")
	// force overrides the unmerged half-removed refusal → stub.
	requireStub(t, disposeErr(t, r, id, true, false))
}

func TestDispose_HalfRemovedUnchanged_Stub(t *testing.T) {
	// A half-removed lane whose branch tip == base (unchanged) is collectible.
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", lanePath)
	requireStub(t, disposeErr(t, r, id, false, false))
}

func TestDispose_AlreadyDisposed_IdempotentCleanup(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedIsolationLane(t)
	// Mark disposed but leave branch + sidecar as remnants (crash between mark
	// and cleanup). A re-issued dispose is a no-op that clears them.
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventDelegateDisposed, TS: time.Now().UTC(), DelegateID: id,
	}); err != nil {
		t.Fatalf("append disposed: %v", err)
	}
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

func TestDispose_SessionClosing_Refused(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedIsolationLane(t)
	r.s.mu.Lock()
	r.s.closing = true
	r.s.mu.Unlock()
	requireRefusalContains(t, disposeErr(t, r, id, false, false), "session is closing")
}

// deleteRunning removes a test-injected fake running job so the session's close
// cleanup does not try to finalize an incomplete runningJob.
func deleteRunning(r *wtRepo, jobID string) {
	r.s.jobManager.mu.Lock()
	delete(r.s.jobManager.running, jobID)
	r.s.jobManager.mu.Unlock()
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// laneCommit makes a real commit on the lane's branch so the lane is ahead of
// its base (unmerged).
func laneCommit(t *testing.T, lanePath string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(lanePath, "work.txt"), "lane work")
	wtGit(t, lanePath, "add", "-A")
	wtGit(t, lanePath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "lane work")
}
