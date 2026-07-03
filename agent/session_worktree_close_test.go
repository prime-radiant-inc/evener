package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// worktreeInternalDir locates the .git/worktrees/<id> directory that
// registers the linked worktree at lanePath, by matching each candidate's
// reverse "gitdir" file (the absolute path to lanePath's own ".git" file)
// rather than assuming the directory is named after the lane — git dedups
// the internal directory name on a collision, so the lane's own name is not
// a safe assumption.
func worktreeInternalDir(t *testing.T, mainRoot, lanePath string) string {
	t.Helper()
	base := filepath.Join(mainRoot, ".git", "worktrees")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
	}
	want := filepath.Clean(filepath.Join(lanePath, ".git"))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(base, e.Name(), "gitdir"))
		if err != nil {
			continue
		}
		if filepath.Clean(strings.TrimSpace(string(b))) == want {
			return filepath.Join(base, e.Name())
		}
	}
	t.Fatalf("no .git/worktrees entry registers lane %s", lanePath)
	return ""
}

// chmodReadOnly strips write permission from dir (kept readable/searchable)
// so git's own attempt to create or remove its "locked" marker file inside
// fails with a genuine permission error, while earlier read-only git calls
// (status, rev-parse, worktree list) over the same tree are unaffected. It
// restores the original mode via t.Cleanup so t.TempDir()'s own removal
// still succeeds.
func chmodReadOnly(t *testing.T, dir string) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("running as root; a chmod-based permission test would not fail")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// gitFailOnArgsShim installs a PATH-shimmed `git` that fails with a
// synthetic error whenever its argv exactly matches failArgs, forwarding
// every other invocation to the real git binary. Lets a test fail one
// specific git subcommand deep inside a lifecycle op while every other call
// on the same path (including earlier ones in the same call) still runs for
// real.
func gitFailOnArgsShim(t *testing.T, failArgs ...string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	match := strings.Join(failArgs, " ")
	script := "#!/bin/sh\n" +
		"if [ \"$*\" = '" + match + "' ]; then echo 'shim: forced failure' >&2; exit 1; fi\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gitFailOnNthMatchingCallShim installs a PATH-shimmed `git` that fails only
// the n-th invocation (1-indexed) whose argv exactly matches matchArgs,
// forwarding every other invocation — including earlier matches — to the
// real git binary. Used when the identical git subcommand runs more than
// once in a single code path and only a later call must fail.
func gitFailOnNthMatchingCallShim(t *testing.T, matchArgs string, n int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	counter := filepath.Join(shimDir, "count")
	script := "#!/bin/sh\n" +
		"if [ \"$*\" = '" + matchArgs + "' ]; then\n" +
		"  c=$(cat '" + counter + "' 2>/dev/null || echo 0)\n" +
		"  c=$((c+1))\n" +
		"  echo \"$c\" > '" + counter + "'\n" +
		"  if [ \"$c\" -ge " + strconv.Itoa(n) + " ]; then\n" +
		"    echo 'shim: forced failure' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"fi\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// hideGitEntirely removes git from PATH for the rest of the test (proving
// there really is no git binary reachable, mirroring
// TestWorktreeErrors_GitUnavailableLifecycleOpsErrorClearly), and returns a
// restore func a caller must invoke before any post-call assertion that
// itself shells out to git (e.g. through wtGit/laneLocked/branchExists).
func hideGitEntirely(t *testing.T) (restore func()) {
	t.Helper()
	orig := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("git"); err == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}
	return func() { t.Setenv("PATH", orig) }
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

// --- ownedIsolationLanes: pure decision-core coverage ---

// TestOwnedIsolationLanes_SkipsForeignParentSessionID: a delegate restore
// descriptor whose ParentSessionID names a DIFFERENT session (a forwarded
// copy of a descendant's own delegate, or simply another session's lane)
// must never be enumerated as a lane this session created and may dispose.
func TestOwnedIsolationLanes_SkipsForeignParentSessionID(t *testing.T) {
	recs := map[string]*jobstore.JobRecord{
		"job1": {
			DelegateID: "dlg1",
			DelegateRestore: &jobstore.DelegateRestoreDescriptor{
				Isolation:       "worktree",
				ParentSessionID: "some-other-session",
				WorkingDir:      "/tmp/somewhere",
			},
		},
	}
	lanes := ownedIsolationLanes(recs, "this-session")
	if len(lanes) != 0 {
		t.Errorf("lanes = %+v, want none (ParentSessionID belongs to a different session)", lanes)
	}
}

// --- disposeOneDelegateLane: gaps left by the happy-path tests above ---

// TestDisposeOneDelegateLane_MissingSidecarLeavesLane: without a sidecar the
// recorded base SHA is unknown, so the lane's provenance cannot be judged —
// disposal must leave it entirely untouched (still locked, still resumable).
func TestDisposeOneDelegateLane_MissingSidecarLeavesLane(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(r.canonicalMain(t))
	if err := worktree.DeleteSidecar(metaDir, delegateID); err != nil {
		t.Fatalf("delete sidecar: %v", err)
	}

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("lane wrongly removed without a sidecar: %v", err)
	}
	reg, locked, reason := r.laneLocked(t, lanePath)
	if !reg || !locked {
		t.Fatalf("lane must stay registered and locked, got reg=%v locked=%v", reg, locked)
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != delegateID {
		t.Errorf("lock reason = %q, want the lane's own dlg marker untouched", reason)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite an unjudgeable (no-sidecar) lane")
	}
}

// TestDisposeOneDelegateLane_LockStateUnverifiableLeavesLane: when the git
// call lockStateOf needs (`worktree list --porcelain`) itself fails, the lane
// is left entirely alone rather than guessed at.
func TestDisposeOneDelegateLane_LockStateUnverifiableLeavesLane(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	restore := hideGitEntirely(t)

	r.s.disposeDelegateLanesAtClose()

	restore()
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("lane wrongly removed when the lock state could not be verified: %v", err)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite an unverifiable lock state")
	}
}

// TestDisposeOneDelegateLane_UnresolvableMainRootLeavesLane: a lane whose own
// ".git" pointer no longer resolves to a main repo root (corrupted content,
// with git unavailable for the binary fallback) is left alone rather than
// guessed at.
func TestDisposeOneDelegateLane_UnresolvableMainRootLeavesLane(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	if err := os.WriteFile(filepath.Join(lanePath, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitEntirely(t)

	r.s.disposeDelegateLanesAtClose()

	restore()
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite an unresolvable main root")
	}
	_, locked, _ := r.laneLocked(t, lanePath)
	if !locked {
		t.Error("lane unlocked despite an unresolvable main root (should be left entirely alone)")
	}
}

// TestDisposeOneDelegateLane_UnchangedCheckFailsKeepsAndUnlocks: when
// worktree.Unchanged itself cannot be evaluated (its `status` call fails
// while the rest of the lifecycle git calls succeed), disposal fails safe
// toward preservation: unlock our own lock and keep the lane resumable.
func TestDisposeOneDelegateLane_UnchangedCheckFailsKeepsAndUnlocks(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	gitFailOnArgsShim(t, "-C", lanePath, "status", "--porcelain=v1", "--untracked-files=all")

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("lane wrongly removed when Unchanged could not be evaluated: %v", err)
	}
	if _, locked, _ := r.laneLocked(t, lanePath); locked {
		t.Error("lane still locked; an unverifiable state must still release our own lock")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, delegateID, "state unverifiable") {
		t.Errorf("close output did not report the state-unverifiable lane: %v", msgs)
	}
}

// TestDisposeOneDelegateLane_ChangedForeignLockDeclinedNotTouched: a changed
// lane whose lock is no longer the disposer's own serf:dlg: marker (someone
// switched into it) is declined — left completely untouched, not unlocked
// and not reported as kept.
func TestDisposeOneDelegateLane_ChangedForeignLockDeclinedNotTouched(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	// Someone switched into the lane after creation: unlock the dlg marker and
	// relock with a foreign session marker.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "serf:someone-else-session", lanePath)

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("foreign-locked changed lane wrongly removed: %v", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked || reason != "serf:someone-else-session" {
		t.Errorf("lock = (%v,%q), want the foreign lock left untouched", locked, reason)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite a declined (foreign-locked) lane")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("foreign-locked lane wrongly marked disposed")
	}
}

// TestDisposeOneDelegateLane_ChangedLaneUnlockFailsLeavesLocked: a changed
// lane still carries our own dlg lock, but the `worktree unlock` command
// itself fails (permission denied writing the internal marker file) — the
// lane is left locked and resumable, not silently downgraded.
func TestDisposeOneDelegateLane_ChangedLaneUnlockFailsLeavesLocked(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), lanePath)
	chmodReadOnly(t, internalDir)

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("changed lane wrongly removed when unlock failed: %v", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Error("lane must remain locked when the unlock command itself fails")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != delegateID {
		t.Errorf("lock reason = %q, want the lane's own dlg marker still present", reason)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite a lock that could not be released")
	}
}

// TestDisposeOneDelegateLane_UnchangedUnlockFailsLeavesLocked: an unchanged
// lane's own `worktree unlock` (the EvDisposeUnchanged ActUnlock step, before
// remove is even attempted) fails — the lane is left locked and NOT removed.
func TestDisposeOneDelegateLane_UnchangedUnlockFailsLeavesLocked(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), lanePath)
	chmodReadOnly(t, internalDir)

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("unchanged lane wrongly removed when its unlock failed: %v", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Error("lane must remain locked when the unchanged-path unlock fails")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != delegateID {
		t.Errorf("lock reason = %q, want the lane's own dlg marker still present", reason)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite a lock that could not be released")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("lane wrongly marked disposed when removal never happened")
	}
}

// TestDisposeOneDelegateLane_DisposedMarkAppendFailureWarnsButStillRemoves:
// the `git worktree remove` already succeeded — the lane is gone — before the
// disposed-mark append is attempted; if that append itself fails, disposal
// still proceeds with branch + sidecar cleanup (best-effort) and surfaces a
// warning naming the failure.
func TestDisposeOneDelegateLane_DisposedMarkAppendFailureWarnsButStillRemoves(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(r.canonicalMain(t))
	origAppend := r.s.jobManager.appendEvent
	markErr := errors.New("disk full")
	r.s.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventDelegateDisposed {
			return markErr
		}
		return origAppend(e)
	}
	defer func() { r.s.jobManager.appendEvent = origAppend }()

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); !os.IsNotExist(err) {
		t.Errorf("lane worktree still present after removal: err=%v", err)
	}
	if r.branchExists(t, delegateID) {
		t.Error("branch should still be deleted even when the disposed mark failed to append")
	}
	if _, err := worktree.ReadSidecar(metaDir, delegateID); err == nil {
		t.Error("sidecar should still be deleted even when the disposed mark failed to append")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, delegateID, "disposal mark failed") {
		t.Errorf("no warning about the failed disposed mark: %v", msgs)
	}
}

// TestDisposeOneDelegateLane_BranchDeleteFailureWarnsButLaneStillGone: the
// worktree and disposed mark both succeed; only `git branch -D` fails. The
// lane is still gone (unrevivable) and the sidecar is still cleaned up
// (best-effort); a warning names the leaked branch.
func TestDisposeOneDelegateLane_BranchDeleteFailureWarnsButLaneStillGone(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(r.canonicalMain(t))
	gitFailOnArgsShim(t, "branch", "-D", delegateID)

	r.s.disposeDelegateLanesAtClose()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); !os.IsNotExist(err) {
		t.Errorf("lane worktree still present after removal: err=%v", err)
	}
	if !r.disposedEventPresent(t, delegateID) {
		t.Error("disposed mark must still be durable even when branch delete failed")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch should still exist since its delete was made to fail")
	}
	if _, err := worktree.ReadSidecar(metaDir, delegateID); err == nil {
		t.Error("sidecar still present after disposal")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, delegateID, "branch delete failed") {
		t.Errorf("no warning about the failed branch delete: %v", msgs)
	}
}

// --- unlockOwnManagedWorktreeAtClose: gaps left by TestClose_UnlocksOwnManagedWorktree ---

// TestUnlockOwnManagedWorktreeAtClose_NonLocalEnvNoOp: close-unlock is a
// local-execution-environment-only feature; a non-local env leaves the
// worktree exactly as it was (still locked).
func TestUnlockOwnManagedWorktreeAtClose_NonLocalEnvNoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r.s.mu.Lock()
	path := r.s.worktreeCurrentPath
	r.s.env = &timeoutEnv{wd: path}
	r.s.mu.Unlock()

	r.s.unlockOwnManagedWorktreeAtClose()

	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Error("own worktree wrongly unlocked through a non-local env")
	}
}

// TestUnlockOwnManagedWorktreeAtClose_UnresolvableMainRootNoOp: the session's
// own occupied worktree no longer resolves to a main repo root (corrupted
// ".git" pointer, git unavailable for the binary fallback) — close-unlock
// leaves it alone rather than guessing.
func TestUnlockOwnManagedWorktreeAtClose_UnresolvableMainRootNoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitEntirely(t)

	r.s.unlockOwnManagedWorktreeAtClose()

	restore()
	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Error("own worktree wrongly unlocked despite an unresolvable main root")
	}
}

// TestUnlockOwnManagedWorktreeAtClose_LeaveFailsWarns: leaveCurrentWorktree's
// own git call fails (git unavailable) — the worktree stays locked and a
// warning names the failure, rather than the failure being swallowed.
func TestUnlockOwnManagedWorktreeAtClose_LeaveFailsWarns(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	restore := hideGitEntirely(t)

	r.s.unlockOwnManagedWorktreeAtClose()

	restore()
	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Error("own worktree wrongly unlocked when the unlock attempt failed")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "unlocking own worktree", "failed") {
		t.Errorf("no warning about the failed close-unlock: %v", msgs)
	}
}
