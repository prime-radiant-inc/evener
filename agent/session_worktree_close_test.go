package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
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
	path, _, base, _, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
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

func writeRepoGitShim(t *testing.T, repoRoot, script string) func() {
	t.Helper()
	shimDir := filepath.Join(repoRoot, ".venv", "bin")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatalf("mkdir repo git shim dir: %v", err)
	}
	shim := filepath.Join(shimDir, "git")
	writeExecUnderForkLock(t, shim, script)
	return func() { _ = os.Remove(shim) }
}

// writeExecUnderForkLock writes an executable that this package then runs, in a
// way that is safe under the parallel test suite's constant fork/exec traffic.
//
// A freshly written executable can fail execve with ETXTBSY ("text file busy"):
// os.WriteFile holds the file open for writing, and if any sibling parallel test
// forks for its own os/exec during that window, the forked child inherits the
// still-open write fd. Until that child execs, the kernel sees this file as open
// for writing and refuses to execute it. On Linux, ExecArgv surfaces the failed
// cmd.Start() as a bare exit 127 (the underlying "text file busy" is discarded),
// which is how this manifests in the worktree git-shim tests. See Go issue
// #22315.
//
// syscall.ForkLock is the standard guard for exactly this: fork/exec takes it
// for writing (forkExec -> acquireForkLock), so holding it for reading across
// the whole write excludes any concurrent fork. Once the write completes and the
// fd is closed, forks resume and no child can inherit it. This keeps every test
// parallel while removing the race at its source.
func writeExecUnderForkLock(t *testing.T, path, script string) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func gitFailOnArgsRepoShim(t *testing.T, repoRoot string, failArgs ...string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	match := strings.Join(failArgs, " ")
	script := "#!/bin/sh\n" +
		"if [ \"$*\" = '" + match + "' ]; then echo 'shim: forced failure' >&2; exit 1; fi\n" +
		"exec '" + realGit + "' \"$@\"\n"
	writeRepoGitShim(t, repoRoot, script)
}

func hideGitInRepo(t *testing.T, repoRoot string) func() {
	t.Helper()
	script := "#!/bin/sh\necho 'shim: git hidden' >&2\nexit 127\n"
	return writeRepoGitShim(t, repoRoot, script)
}

// --- Step 4: disposal ---

// TestDisposeUnchangedLane_RemovedAndMarked: an unchanged lane at close is
// removed (worktree + branch + sidecar + lock all gone) and the descriptor is
// marked disposed.
func TestDisposeUnchangedLane_RemovedAndMarked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
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

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	// Uncommitted (dirty) change only — no commits beyond base.
	if err := os.WriteFile(filepath.Join(lanePath, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r.s.disposeDelegateLanesAtClose(context.Background())

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

// TestDisposeKeptLane_UnreadableStateReportedUnknown: the kept-lane note is
// what a model reads to decide whether a preserved lane still matters. Its
// ahead/dirty reads are best-effort, and their zero values render as
// "0 ahead, dirty=false" — the description of a lane with nothing in it. A read
// that failed must say so instead. The note itself is never suppressed: unlike
// the delegate report (isolatedDelegateWorktreeReport, which returns nil and
// emits nothing), this note is the only announcement that the lane was kept at
// all, so dropping it would hide preserved work outright.
func TestDisposeKeptLane_UnreadableStateReportedUnknown(t *testing.T) {
	t.Run("ahead count unreadable", func(t *testing.T) {
		t.Parallel()
		r := newWorktreeRepo(t)
		delegateID, lanePath, baseSHA := r.seedIsolationLane(t)
		if err := os.WriteFile(filepath.Join(lanePath, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// rev-list --count is not part of the disposal predicate, so failing it
		// from the first call reaches the note-rendering read directly.
		scriptedGitRunner(r.s, failGitArgsFrom(1, revListArgv(lanePath, baseSHA)...))

		r.s.disposeDelegateLanesAtClose(context.Background())

		msgs := warningMessages(r.s)
		if !anyContainsAll(msgs, delegateID, "ahead unknown") {
			t.Errorf("kept-lane note did not report the unreadable ahead count as unknown: %v", msgs)
		}
		if anyContainsAll(msgs, delegateID, "0 ahead") {
			t.Errorf("kept-lane note reported an unreadable ahead count as 0 ahead: %v", msgs)
		}
	})

	// The dirty read at the note is a REPEAT read: the disposal predicate
	// already ran `status` twice for this lane (worktree.Unchanged's and
	// laneAutoCollectible's own CleanTree). Failing every `status` would make
	// the predicate bail first down the "state unverifiable" branch, so this
	// fails from the third match onward — the shape a spent close budget has,
	// where the predicate completed and the render-time read no longer can.
	t.Run("dirty state unreadable at render time", func(t *testing.T) {
		t.Parallel()
		r := newWorktreeRepo(t)
		delegateID, lanePath, _ := r.seedIsolationLane(t)
		if err := os.WriteFile(filepath.Join(lanePath, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		scriptedGitRunner(r.s, failGitArgsFrom(3, statusArgv(lanePath)...))

		r.s.disposeDelegateLanesAtClose(context.Background())

		msgs := warningMessages(r.s)
		if anyContainsAll(msgs, delegateID, "state unverifiable") {
			t.Fatalf("predicate bailed before the render-time read; the third status call was not the one under test: %v", msgs)
		}
		if !anyContainsAll(msgs, delegateID, "dirty unknown") {
			t.Errorf("kept-lane note did not report the unreadable tree state as unknown: %v", msgs)
		}
		if anyContainsAll(msgs, delegateID, "dirty=false") {
			t.Errorf("kept-lane note reported an unreadable tree as clean: %v", msgs)
		}
	})
}

// TestDisposeRacingDirtyWrite_DowngradesToKeepUnlocked: the non-force remove
// refuses because a write raced the clean check → downgrade to keep. At CLOSE
// the policy is downgradeUnlockKeep (spec §P0: a dead owner whose lock nobody
// would ever release again), so the kept lane is left UNLOCKED, not re-locked.
func TestDisposeRacingDirtyWrite_DowngradesToKeepUnlocked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	// Seam: dirty the lane immediately before the non-force remove so git
	// refuses it, exercising the downgrade path.
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	r.s.disposeDelegateLanesAtClose(context.Background())

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("lane removed despite racing dirty write: %v", err)
	}
	reg, locked, _ := r.laneLocked(t, lanePath)
	if !reg {
		t.Fatal("lane deregistered after downgrade")
	}
	if locked {
		t.Error("close-path downgrade must leave the lane UNLOCKED (dead owner), not re-locked")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("downgraded lane branch deleted; must stay resumable")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("downgraded lane wrongly marked disposed")
	}
}

// TestDisposeAncestryMergedLane_Collected: a lane with commits that are an
// ancestor of the LOCAL merge_target branch (a real merge) is auto-collectible
// at close (spec §P0 / §D0-auto): removed, disposed-marked, branch + sidecar
// gone. An unchanged lane in the same close pass is still collected.
func TestDisposeAncestryMergedLane_Collected(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	mergedID, mergedPath, _ := r.seedIsolationLane(t)
	unchangedID, unchangedPath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))

	// Commit twice in the merged lane, then fast-forward main to the lane tip so
	// the lane's commits are reachable from refs/heads/main (ancestry-merged).
	if err := os.WriteFile(filepath.Join(mergedPath, "work.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, mergedPath, "add", "work.txt")
	wtGit(t, mergedPath, "commit", "-m", "merged lane work")
	wtGit(t, r.mainRoot, "merge", "--ff-only", mergedID)

	r.s.disposeDelegateLanesAtClose(context.Background())

	// Merged lane collected.
	if _, err := os.Stat(filepath.Join(mergedPath, ".git")); !os.IsNotExist(err) {
		t.Errorf("ancestry-merged lane still present after disposal: err=%v", err)
	}
	if r.branchExists(t, mergedID) {
		t.Error("ancestry-merged lane branch still exists after disposal")
	}
	if _, err := worktree.ReadSidecar(metaDir, mergedID); err == nil {
		t.Error("ancestry-merged lane sidecar still present after disposal")
	}
	if !r.disposedEventPresent(t, mergedID) {
		t.Error("ancestry-merged lane not marked disposed")
	}
	// Unchanged lane still collected in the same pass.
	if _, err := os.Stat(filepath.Join(unchangedPath, ".git")); !os.IsNotExist(err) {
		t.Errorf("unchanged lane still present after disposal: err=%v", err)
	}
	if !r.disposedEventPresent(t, unchangedID) {
		t.Error("unchanged lane not marked disposed")
	}
}

// gitLogRepoShim installs a git shim that appends every invocation's argv to a
// log file and execs real git, so a test can assert which subcommands ran.
func gitLogRepoShim(t *testing.T, repoRoot string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	logPath := filepath.Join(t.TempDir(), "gitcalls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + logPath + "'\n" +
		"exec '" + realGit + "' \"$@\"\n"
	writeRepoGitShim(t, repoRoot, script)
	return logPath
}

func assertNoGitCherry(t *testing.T, logPath string) {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("git call log %s missing; the git shim intercepted no calls, so the no-cherry assertion is vacuous: %v", logPath, err)
	}
	sawCall := false
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sawCall = true
		if line == "cherry" || strings.HasPrefix(line, "cherry ") {
			t.Fatalf("git cherry must never run at close; call log:\n%s", string(b))
		}
	}
	if !sawCall {
		t.Fatalf("git call log %s is empty; the git shim intercepted no calls, so the no-cherry assertion is vacuous", logPath)
	}
}

// TestDisposeCherryOnlyMergedLane_KeptNoCherry: a lane whose commit is
// patch-equivalent to a commit on main but NOT an ancestor of it (a
// cherry-pick / squash) is NOT auto-collectible (spec §D0-auto: ancestry arm
// only). It is KEPT, and close never runs `git cherry`.
func TestDisposeCherryOnlyMergedLane_KeptNoCherry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)

	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("patch\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	laneTip := strings.TrimSpace(wtGit(t, lanePath, "rev-parse", "HEAD"))
	// Diverge main with its own commit first, then cherry-pick the lane commit
	// onto it: main gains a patch-equivalent commit with a DIFFERENT parent (and
	// SHA), so the lane tip is patch-equal-but-NOT-an-ancestor of main.
	if err := os.WriteFile(filepath.Join(r.mainRoot, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, r.mainRoot, "add", "other.txt")
	wtGit(t, r.mainRoot, "commit", "-m", "diverge main")
	wtGit(t, r.mainRoot, "cherry-pick", laneTip)

	logPath := gitLogRepoShim(t, r.mainRoot)
	r.s.disposeDelegateLanesAtClose(context.Background())

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("cherry-only-merged lane wrongly removed: %v", err)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("cherry-only-merged lane branch deleted; must stay resumable")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("cherry-only-merged lane wrongly marked disposed")
	}
	assertNoGitCherry(t, logPath)
}

// TestDisposeRemoteTrackingOnlyMergeTarget_Kept: a lane whose merge_target
// resolves ONLY to a remote-tracking ref (no local branch) is NOT
// auto-collectible (spec §D0-auto: remote-tracking evidence is not
// auto-trustworthy) — KEPT, and `git cherry` never runs.
func TestDisposeRemoteTrackingOnlyMergeTarget_Kept(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))

	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("feat\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	// Publish the lane tip as origin/feature (a remote-tracking ref that
	// contains the lane) with NO local refs/heads/feature branch.
	remoteDir := filepath.Join(t.TempDir(), "origin.git")
	wtGit(t, r.mainRoot, "init", "-q", "--bare", remoteDir)
	wtGit(t, r.mainRoot, "remote", "add", "origin", remoteDir)
	wtGit(t, r.mainRoot, "push", "-q", "origin", delegateID+":feature")
	wtGit(t, r.mainRoot, "fetch", "-q", "origin")
	if err := worktree.UpdateSidecar(metaDir, delegateID, func(sc *worktree.Sidecar) {
		sc.MergeTarget = "feature"
	}); err != nil {
		t.Fatalf("update sidecar merge_target: %v", err)
	}

	logPath := gitLogRepoShim(t, r.mainRoot)
	r.s.disposeDelegateLanesAtClose(context.Background())

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("remote-tracking-only merge_target lane wrongly removed: %v", err)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("remote-tracking-only lane branch deleted; must stay resumable")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("remote-tracking-only lane wrongly marked disposed")
	}
	assertNoGitCherry(t, logPath)
}

// TestKeepWarningCopy_LumpedWording: the close-time KEEP warning uses the
// lumped wording (spec §P0: close cannot distinguish cherry-merged from
// unmerged without the banned cherry test).
func TestKeepWarningCopy_LumpedWording(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	_, lanePath, _ := r.seedIsolationLane(t)
	// A dirty (unmerged) lane so it is KEPT and listed in the warning.
	if err := os.WriteFile(filepath.Join(lanePath, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r.s.disposeDelegateLanesAtClose(context.Background())

	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, "not collected automatically (unmerged or squash-merged), dirty, or unverifiable") {
		t.Errorf("KEEP warning does not use the lumped wording: %v", msgs)
	}
}

// TestDisposeKeptLane_TouchesSidecarBeforeUnlock: every KEEP path refreshes the
// lane's sidecar (spec §P0: P3's residue-collection grace keys on the touch)
// before releasing the lock. A changed lane exercises the changed-keep path.
func TestDisposeKeptLane_TouchesSidecarBeforeUnlock(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	// Backdate the sidecar so a fresh touch is unambiguously detectable.
	scPath := filepath.Join(metaDir, worktree.EncodeSidecarName(delegateID)+".json")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(scPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	r.s.disposeDelegateLanesAtClose(context.Background())

	info, err := os.Stat(scPath)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if !info.ModTime().After(old) {
		t.Errorf("kept lane's sidecar was not touched (mtime %v not after %v)", info.ModTime(), old)
	}
	// And the lane is still unlocked + resumable.
	if _, locked, _ := r.laneLocked(t, lanePath); locked {
		t.Error("kept changed lane still locked")
	}
}

// TestResumeAfterP0Disposal_Refused: after P0 collects an ancestry-merged lane,
// a later resume of that delegate hits the disposed refusal (spec §P0 → the
// existing assessDelegateResumability path).
func TestResumeAfterP0Disposal_Refused(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)

	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "merged lane work")
	wtGit(t, r.mainRoot, "merge", "--ff-only", delegateID)

	r.s.disposeDelegateLanesAtClose(context.Background())

	recs, err := r.s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var rec *jobstore.JobRecord
	for _, candidate := range recs {
		if candidate.DelegateID == delegateID {
			rec = candidate
		}
	}
	if rec == nil {
		t.Fatal("no job record for the disposed delegate")
	}
	a := r.s.assessDelegateResumability(rec, delegateResumabilityProjection)
	if a.Resumable || a.Reason != notResumableWorktreeDisposed {
		t.Fatalf("assessment = %+v, want not-resumable with %s", a, notResumableWorktreeDisposed)
	}
}

// laneDisposalRunner builds the git control runner and observed lock state for
// a seeded lane, mirroring disposeOneDelegateLane's setup, so a test can drive
// disposeUnchangedLaneMechanics directly with either downgrade policy.
func (r *wtRepo) laneDisposalRunner(t *testing.T, lane isolationLane) (worktree.GitRunner, worktree.LockState) {
	t.Helper()
	local, ok := r.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("session env is not a LocalExecutionEnvironment")
	}
	lanePath := filepath.Clean(lane.path)
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		t.Fatal("could not resolve main repo root for lane")
	}
	run := r.s.newWorktreeGitRunner(context.Background(), local.WithWorkingDirectory(mainRoot))
	locked, reason, err := lockStateOf(run, lanePath)
	if err != nil {
		t.Fatalf("lockStateOf: %v", err)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, r.s.id, lane.delegateID)
	}
	return run, st
}

// TestDisposeUnchangedLaneMechanics_RelockPolicy: a late dirty write races the
// clean check so git refuses the non-force remove; under downgradeRelockKeep the
// helper re-locks the lane with the disposer's own serf:dlg marker and keeps it.
func TestDisposeUnchangedLaneMechanics_RelockPolicy(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	lane := isolationLane{delegateID: delegateID, path: lanePath}
	metaDir := r.metaDir(t, r.canonicalMain(t))
	run, st := r.laneDisposalRunner(t, lane)
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	outcome, note := r.s.disposeUnchangedLaneMechanics(run, st, lane, metaDir, downgradeRelockKeep, false)

	if outcome != laneKeptDirty {
		t.Fatal("relock policy: lane not kept after refused remove")
	}
	if !strings.Contains(note, "re-locked") {
		t.Errorf("relock policy note = %q, want it to mention re-locked", note)
	}
	reg, locked, reason := r.laneLocked(t, lanePath)
	if !reg {
		t.Fatal("relock policy: lane deregistered")
	}
	if !locked {
		t.Error("relock policy: lane not re-locked")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.DelegateID != delegateID {
		t.Errorf("relock policy: reason = %q, want serf:dlg marker for %s", reason, delegateID)
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("relock policy: lane wrongly marked disposed")
	}
}

// TestDisposeUnchangedLaneMechanics_UnlockPolicy: same refused-remove race, but
// under downgradeUnlockKeep the helper leaves the kept lane UNLOCKED (a dead
// owner at close whose lock nobody would ever release) and never marks disposed.
func TestDisposeUnchangedLaneMechanics_UnlockPolicy(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	lane := isolationLane{delegateID: delegateID, path: lanePath}
	metaDir := r.metaDir(t, r.canonicalMain(t))
	run, st := r.laneDisposalRunner(t, lane)
	r.s.worktreeDisposeBeforeRemove = func(p string) {
		_ = os.WriteFile(filepath.Join(p, "raced.txt"), []byte("late\n"), 0o644)
	}

	outcome, note := r.s.disposeUnchangedLaneMechanics(run, st, lane, metaDir, downgradeUnlockKeep, false)

	if outcome != laneKeptDirty {
		t.Fatal("unlock policy: lane not kept after refused remove")
	}
	if !strings.Contains(note, "kept unlocked") {
		t.Errorf("unlock policy note = %q, want it to mention kept unlocked", note)
	}
	reg, locked, _ := r.laneLocked(t, lanePath)
	if !reg {
		t.Fatal("unlock policy: lane deregistered")
	}
	if locked {
		t.Error("unlock policy: lane still locked; a dead owner's lane must be left unlocked")
	}
	if !r.branchExists(t, delegateID) {
		t.Error("unlock policy: lane branch deleted; must stay resumable")
	}
	if r.disposedEventPresent(t, delegateID) {
		t.Error("unlock policy: lane wrongly marked disposed")
	}
}

// TestClose_UnlocksOwnManagedWorktree: a clean close unlocks the session's own
// occupied managed worktree on disk (spec §5 close-unlock).
func TestClose_UnlocksOwnManagedWorktree(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if err == nil || !containsAll(err.Error(), "disposed", "start a new delegate") {
		t.Errorf("send error = %v, want a clear disposed message", err)
	}
	// Mid-life dispose exists now, so the copy must not claim WHEN it happened.
	if strings.Contains(err.Error(), "at session close") {
		t.Errorf("send error = %q, must not claim disposal happened at session close", err.Error())
	}
}

// TestResumability_RefusesMissingWorkingDir: the unconditional WorkingDir stat
// (crash net) refuses restoration into a deleted directory, covering the crash
// window between remove and mark.
func TestResumability_RefusesMissingWorkingDir(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))
	if err := worktree.DeleteSidecar(metaDir, delegateID); err != nil {
		t.Fatalf("delete sidecar: %v", err)
	}

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	restore := hideGitInRepo(t, r.mainRoot)

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	if err := os.WriteFile(filepath.Join(lanePath, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitInRepo(t, r.mainRoot)

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	gitFailOnArgsRepoShim(t, r.mainRoot, "-C", lanePath, "status", "--porcelain=v1", "--untracked-files=all")

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
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

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), lanePath)
	chmodReadOnly(t, internalDir)

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), lanePath)
	chmodReadOnly(t, internalDir)

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))
	origAppend := r.s.jobManager.appendEvent
	markErr := errors.New("disk full")
	r.s.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventDelegateDisposed {
			return markErr
		}
		return origAppend(e)
	}
	defer func() { r.s.jobManager.appendEvent = origAppend }()

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	metaDir := r.metaDir(t, r.canonicalMain(t))
	gitFailOnArgsRepoShim(t, r.mainRoot, "branch", "-D", delegateID)

	r.s.disposeDelegateLanesAtClose(context.Background())

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
	t.Parallel()
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
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitInRepo(t, r.mainRoot)

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
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	restore := hideGitInRepo(t, r.mainRoot)

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

// --- Budget + budget-exempt tail (spec §P0, Constants, test 4) ---

// lanePresent reports whether the lane's linked worktree is still on disk.
func lanePresent(lanePath string) bool {
	_, err := os.Stat(filepath.Join(lanePath, ".git"))
	return err == nil
}

// TestCloseBudget_ExhaustedMidPass_LanesLeftSafe: when the shared deadline
// context is cancelled while the pass is running (during the first lane's
// initial git query),
// every lane is left SAFE — present, unlocked (never left locked), and not
// disposed — and the close emits one aggregated tail warning. A budget that
// expires mid-dispose routes that lane through the budget-exempt touch+unlock
// tail rather than leaving it locked; the remaining lanes hit the same tail.
func TestCloseBudget_ExhaustedMidPass_LanesLeftSafe(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	idA, pathA, _ := r.seedIsolationLane(t)
	idB, pathB, _ := r.seedIsolationLane(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	cancelled := false
	r.s.cfg.testOnly.worktreeGitRunner = func(runCtx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		run := gitRunner(runCtx, env)
		return func(args ...string) (string, error) {
			if !cancelled && len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
				cancelled = true
				cancel()
				return "", runCtx.Err()
			}
			return run(args...)
		}
	}
	r.s.disposeDelegateLanesAtClose(ctx)

	for id, path := range map[string]string{idA: pathA, idB: pathB} {
		if !lanePresent(path) {
			t.Errorf("lane %s removed under an expiring budget; expected a safe KEEP/tail", id)
			continue
		}
		if _, locked, _ := r.laneLocked(t, path); locked {
			t.Errorf("lane %s left LOCKED; budget expiry must never leave a lane locked", id)
		}
		if r.disposedEventPresent(t, id) {
			t.Errorf("lane %s wrongly marked disposed under budget expiry", id)
		}
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, "close budget exhausted", "touched+unlocked without disposal") {
		t.Errorf("no aggregated tail warning: %v", msgs)
	}
}

// TestCloseBudget_AlreadyExpired_AllTailedNoDisposal: an incoming ctx whose
// deadline is already in the past (a cascade whose budget was spent by an
// ancestor) is reused, never re-minted, so no lane is evaluated or removed — all
// are touch+unlocked.
func TestCloseBudget_AlreadyExpired_AllTailedNoDisposal(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	idA, pathA, _ := r.seedIsolationLane(t)
	idB, pathB, _ := r.seedIsolationLane(t)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	r.s.disposeDelegateLanesAtClose(ctx)

	for id, path := range map[string]string{idA: pathA, idB: pathB} {
		if !lanePresent(path) {
			t.Errorf("lane %s removed despite an already-expired budget", id)
		}
		if _, locked, _ := r.laneLocked(t, path); locked {
			t.Errorf("lane %s left locked; the tail must unlock", id)
		}
		if r.disposedEventPresent(t, id) {
			t.Errorf("lane %s wrongly disposed under an expired budget", id)
		}
	}
	if !anyContainsAll(warningMessages(r.s), "close budget exhausted") {
		t.Errorf("no tail warning under an expired budget: %v", warningMessages(r.s))
	}
}

// TestCloseBudget_TailExceedsThreshold_Warns: when the touch+unlock tail is
// larger than laneTailWarnThreshold, a second aggregated warning fires. Not
// parallel: it overrides the package-var threshold (parallel tests are paused
// while non-parallel tests run, so the shared var is safe to mutate here).
func TestCloseBudget_TailExceedsThreshold_Warns(t *testing.T) {
	saved := laneTailWarnThreshold
	laneTailWarnThreshold = 1
	defer func() { laneTailWarnThreshold = saved }()

	r := newWorktreeRepo(t)
	r.seedIsolationLane(t)
	r.seedIsolationLane(t)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	r.s.disposeDelegateLanesAtClose(ctx)

	if !anyContainsAll(warningMessages(r.s), "exceeded threshold") {
		t.Errorf("tail of 2 over threshold 1 did not emit the threshold warning: %v", warningMessages(r.s))
	}
}

// TestCloseBudget_Unexpired_NoTailWarning: with the default (ample) budget, an
// unchanged lane is disposed normally and NO tail warning is emitted.
func TestCloseBudget_Unexpired_NoTailWarning(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	_, lanePath, _ := r.seedIsolationLane(t)

	r.s.disposeDelegateLanesAtClose(context.Background())

	if lanePresent(lanePath) {
		t.Error("unchanged lane not disposed under an unexpired budget")
	}
	if anyContainsAll(warningMessages(r.s), "close budget exhausted") {
		t.Errorf("tail warning emitted under an unexpired budget: %v", warningMessages(r.s))
	}
}
