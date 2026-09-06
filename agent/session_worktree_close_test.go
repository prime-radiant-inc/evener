package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
)

// These are REAL-git integration tests for close-time isolation-lane disposal,
// own-worktree close-unlock, and the two revival defenses (native worktree
// tools spec §9 steps 4-6, §5 close-unlock). They build on the wtRepo harness
// from session_tools_worktree_create_test.go.

// seedIsolationLane seeds the stable delegate controller. Returns the delegate
// id, lane path, and the base SHA recorded in the sidecar.
func (r *wtRepo) seedIsolationLane(t *testing.T) (delegateID, lanePath, baseSHA string) {
	t.Helper()
	return r.seedStableIsolationLane(t)
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

func (r *wtRepo) stableDisposalClosurePresent(t *testing.T, delegateID string) bool {
	t.Helper()
	aggregate := delegateAggregateSnapshot(t, r.s.delegateController, delegateID)
	return !aggregate.Resumable && aggregate.NotResumableReason == stableWorktreeDisposalReason
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
// removed (worktree + branch + sidecar + lock all gone) and stable resumability
// is closed.
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
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Error("stable delegate resumability not closed after removal")
	}
}

// TestDisposeChangedLane_UnlockedKept: a lane with commits beyond base is
// unlocked, kept, and left resumable (stable closure absent);
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
	if r.stableDisposalClosurePresent(t, delegateID) {
		t.Error("changed lane wrongly closed stable resumability")
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
		t.Error("downgraded lane branch deleted despite retained residue")
	}
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Error("post-closure dirty race reopened stable resumability")
	}
	if msgs := warningMessages(r.s); !anyContainsAll(msgs, delegateID, "retained residue") {
		t.Errorf("post-closure dirty residue warning missing lane evidence: %v", msgs)
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
	if !r.stableDisposalClosurePresent(t, mergedID) {
		t.Error("ancestry-merged lane not marked disposed")
	}
	// Unchanged lane still collected in the same pass.
	if _, err := os.Stat(filepath.Join(unchangedPath, ".git")); !os.IsNotExist(err) {
		t.Errorf("unchanged lane still present after disposal: err=%v", err)
	}
	if !r.stableDisposalClosurePresent(t, unchangedID) {
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
	if r.stableDisposalClosurePresent(t, delegateID) {
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
	if r.stableDisposalClosurePresent(t, delegateID) {
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

// laneDisposalRunner builds the git control runner and observed lock state for
// a seeded lane, mirroring disposeOneStableDelegateLane's setup, so a test can drive
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
// helper re-locks the lane with the disposer's own evener:dlg marker and keeps it.
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
		t.Errorf("relock policy: reason = %q, want evener:dlg marker for %s", reason, delegateID)
	}
	if r.stableDisposalClosurePresent(t, delegateID) {
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
	if r.stableDisposalClosurePresent(t, delegateID) {
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
// lane and its stable resumability closure is durably present afterward, proving
// disposal ran before the stable controller/store closes.
func TestClose_DisposalRunsBeforeStoreClose(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)

	r.s.Close()

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); !os.IsNotExist(err) {
		t.Errorf("lane not removed by Close: %v", err)
	}
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Error("stable resumability closure not durably present after Close; disposal must run before the stable controller/store closes")
	}
}

// --- disposeOneStableDelegateLane: gaps left by the happy-path tests above ---

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
// lane whose lock is no longer the disposer's own evener:dlg: marker (someone
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
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "evener:someone-else-session", lanePath)

	r.s.disposeDelegateLanesAtClose(context.Background())

	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		t.Errorf("foreign-locked changed lane wrongly removed: %v", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked || reason != "evener:someone-else-session" {
		t.Errorf("lock = (%v,%q), want the foreign lock left untouched", locked, reason)
	}
	if !r.branchExists(t, delegateID) {
		t.Error("branch deleted despite a declined (foreign-locked) lane")
	}
	if r.stableDisposalClosurePresent(t, delegateID) {
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
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Error("post-closure unlock failure reopened stable resumability")
	}
	if msgs := warningMessages(r.s); !anyContainsAll(msgs, delegateID, "lock", "retained residue") {
		t.Errorf("post-closure unlock residue warning missing lock and lane evidence: %v", msgs)
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
	if !r.stableDisposalClosurePresent(t, delegateID) {
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

// TestUnlockOwnManagedWorktreeAtClose_ClearsStrandedOwnMarker: a managed lane
// still carrying this session's own marker while the session occupies a
// different lane is released at close alongside the occupied one. That residue
// is what a crash inside a lane, or a resume whose re-entry was refused, leaves
// behind: the marker names a session id that is dead the moment this close
// finishes, and nothing else would ever release it.
func TestUnlockOwnManagedWorktreeAtClose_ClearsStrandedOwnMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "stranded"})
	if err != nil {
		t.Fatalf("create stranded lane: %v", err)
	}
	strandedPath := first["path"].(string)
	second, err := r.create(t, map[string]any{"name": "current"})
	if err != nil {
		t.Fatalf("create current lane: %v", err)
	}
	currentPath := second["path"].(string)

	// create-away already released the first lane, so re-lock it with this
	// session's marker to stand in for the residue a dead incarnation left.
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)
	if _, locked, _ := r.laneLocked(t, strandedPath); !locked {
		t.Fatal("stranded lane not locked by the test setup")
	}

	r.s.unlockOwnManagedWorktreeAtClose()

	if _, locked, reason := r.laneLocked(t, strandedPath); locked {
		t.Errorf("stranded own marker survived close: locked with %q", reason)
	}
	if _, locked, reason := r.laneLocked(t, currentPath); locked {
		t.Errorf("occupied own lane still locked after close-unlock: %q", reason)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_DelegatingChildReleasesOwnMarker: a
// delegating child keeps the full manage_worktree tool (session_init strips it
// only for worktree-isolated children and for a zero delegation allowance), so
// it does take an evener:<child-sid> marker of its own on a lane it creates or
// switches into. Its teardown must release that marker, and must leave its
// parent's marker on another lane alone.
func TestUnlockOwnManagedWorktreeAtClose_DelegatingChildReleasesOwnMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "parentlane"})
	if err != nil {
		t.Fatalf("create parent lane: %v", err)
	}
	parentLane := first["path"].(string)
	second, err := r.create(t, map[string]any{"name": "childlane"})
	if err != nil {
		t.Fatalf("create child lane: %v", err)
	}
	childLane := second["path"].(string)

	// create-away released the first lane; give it the parent's marker and make
	// this session that parent's child, so the second lane is the child's own.
	parentID := r.s.id + "-parent"
	parentMarker := worktree.FormatSessionMarker(parentID)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", parentMarker, parentLane)
	r.s.cfg.spawn.parentSessionID = parentID
	if !r.s.isSubagentSession() {
		t.Fatal("session did not become a child session")
	}

	r.s.unlockOwnManagedWorktreeAtClose()

	if _, locked, reason := r.laneLocked(t, childLane); locked {
		t.Errorf("child's own marker survived its teardown: locked with %q", reason)
	}
	_, locked, reason := r.laneLocked(t, parentLane)
	if !locked {
		t.Fatal("child released its parent's lock")
	}
	if reason != parentMarker {
		t.Errorf("parent lock reason = %q, want %q", reason, parentMarker)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_BoundedByCloseBudget: the pass runs its
// git on a context carrying its own release budget, so a wedged git holds
// shutdown for that budget and no longer — rather than for the per-command
// timeout on each of the 1+N commands an unbounded pass would issue.
func TestUnlockOwnManagedWorktreeAtClose_BoundedByCloseBudget(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Observe the deadline on every context the pass hands git, and wedge the
	// unlock so the pass also has to report the lane it could not release.
	commands := 0
	unbounded := false
	longest := time.Duration(0)
	t.Cleanup(func() { r.s.cfg.testOnly.worktreeGitRunner = nil })
	r.s.cfg.testOnly.worktreeGitRunner = func(runCtx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		run := gitRunner(runCtx, env)
		return func(args ...string) (string, error) {
			commands++
			deadline, ok := runCtx.Deadline()
			switch {
			case !ok:
				unbounded = true
			default:
				if left := time.Until(deadline); left > longest {
					longest = left
				}
			}
			if len(args) > 1 && args[0] == "worktree" && args[1] == "unlock" {
				return "", errors.New("git wedged")
			}
			return run(args...)
		}
	}

	start := time.Now()
	r.s.unlockOwnManagedWorktreeAtClose()
	elapsed := time.Since(start)

	if unbounded {
		t.Error("the pass handed git a context with no deadline; a wedged git would hold shutdown for the per-command timeout on every lane")
	}
	if commands == 0 {
		t.Fatal("no git command reached the interceptor; the pass never ran")
	}
	if budget := laneCloseReleaseBudget(); longest > budget {
		t.Errorf("git ran with %s left on its deadline, want no more than the release budget %s", longest, budget)
	}
	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Error("lane reported unlocked despite a wedged unlock")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "unlocking own worktree", "failed") {
		t.Errorf("no warning naming the lane the pass could not release: %v", msgs)
	}
	// TRIPWIRE: the wedged unlock fails immediately, so the pass finishes in
	// milliseconds. This bound derives from the release budget rather than a
	// wall-clock literal and sits orders of magnitude above that, so it trips only
	// if the pass starts waiting a budget out instead of honouring its deadline.
	if bound := laneCloseReleaseBudget() / 4; elapsed > bound {
		t.Errorf("pass took %s, want well under %s", elapsed, bound)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_ReleaseBudgetIsNotTheCascades: a real
// Close must leave the lock-release pass holding a budget of its own, not a
// child of the cascade budget. Waiting for the cascade to expire outright is
// not enough to show that: a pass that re-derived from an already-dead cascade
// could still notice at entry and start over. What only an independent budget
// survives is a cascade that is still alive but has less left than the release
// pass needs — so this drives a real close, burns most of the cascade budget
// inside the residue sweep's registry read, and then compares the deadline the
// release pass runs on against the cascade's own. A derived context would carry
// the cascade's earlier deadline; an independent one carries a later deadline of
// its own. Not parallel: shortenCloseCascadeBudget writes a package var.
func TestUnlockOwnManagedWorktreeAtClose_ReleaseBudgetIsNotTheCascades(t *testing.T) {
	cascadeBudget := 800 * time.Millisecond
	shortenCloseCascadeBudget(t, cascadeBudget)
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	// The close issues exactly two registry listings: the P3 residue sweep's,
	// which runs on the shared cascade budget, and then the release pass's. Hold
	// the first until three quarters of the cascade budget is gone — a wait
	// derived from the deadline the sweep was actually handed, not a literal — so
	// the release pass starts while the cascade is alive with less left on it
	// than the release budget. Then record both deadlines from inside the second.
	var listings atomic.Int64
	var cascadeDeadline, releaseDeadline atomic.Int64
	var cascadeLeft atomic.Int64
	var releaseBounded atomic.Bool
	cascadeCtx := make(chan context.Context, 1)
	t.Cleanup(func() { r.s.cfg.testOnly.worktreeGitRunner = nil })
	r.s.cfg.testOnly.worktreeGitRunner = func(runCtx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		run := gitRunner(runCtx, env)
		return func(args ...string) (string, error) {
			if len(args) > 1 && args[0] == "worktree" && args[1] == "list" {
				switch listings.Add(1) {
				case 1:
					deadline, ok := runCtx.Deadline()
					if !ok {
						t.Error("the residue sweep ran without the cascade deadline; this test can no longer burn the cascade budget")
						break
					}
					cascadeCtx <- runCtx
					burn := time.NewTimer(time.Until(deadline) * 3 / 4)
					defer burn.Stop()
					select {
					case <-burn.C:
					case <-runCtx.Done():
					}
				case 2:
					// Compare the two ABSOLUTE deadlines, never the time left on
					// each: an inherited context carries the cascade's own deadline
					// instant, and two time.Until calls a few hundred nanoseconds
					// apart on that one instant differ just enough to read as "later".
					deadline, bounded := runCtx.Deadline()
					releaseBounded.Store(bounded)
					if bounded {
						releaseDeadline.Store(deadline.UnixNano())
					}
					if cascade := <-cascadeCtx; cascade != nil {
						if deadline, ok := cascade.Deadline(); ok {
							cascadeDeadline.Store(deadline.UnixNano())
							cascadeLeft.Store(int64(time.Until(deadline)))
						}
					}
				}
			}
			return run(args...)
		}
	}

	r.s.Close()

	if got := listings.Load(); got != 2 {
		t.Fatalf("close issued %d registry listings, want the residue sweep's then the release pass's", got)
	}
	if !releaseBounded.Load() {
		t.Error("the release pass ran with no deadline at all")
	}
	if left := time.Duration(cascadeLeft.Load()); left <= 0 {
		t.Fatalf("the cascade budget was already gone when the release pass started (%s left), so the window this test needs never opened; the host is too loaded to tell an inherited deadline from an independent one", left)
	}
	if gap := time.Duration(releaseDeadline.Load() - cascadeDeadline.Load()); gap <= 0 {
		t.Errorf("the release pass's deadline sits %s past the cascade's, i.e. on or before it: the pass is bounded by the cascade budget, so a cascade expiring mid-pass would strand the markers", gap)
	}
	if _, locked, reason := r.laneLocked(t, path); locked {
		t.Errorf("own marker still held after the close: %q", reason)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_LeavesDelegateMarker: an evener:dlg: lane
// belongs to the parent's §9 disposal lifecycle, not to this pass, so a delegate
// marker naming this very session as the lane's owner still survives the close.
func TestUnlockOwnManagedWorktreeAtClose_LeavesDelegateMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "dlglane"})
	if err != nil {
		t.Fatalf("create delegate-marked lane: %v", err)
	}
	dlgLane := first["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "ours"}); err != nil {
		t.Fatalf("create own lane: %v", err)
	}
	dlgMarker := worktree.FormatDelegateMarker("dlg_close894", r.s.id)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", dlgMarker, dlgLane)

	r.s.unlockOwnManagedWorktreeAtClose()

	_, locked, reason := r.laneLocked(t, dlgLane)
	if !locked {
		t.Fatal("close released a delegate lane's lock")
	}
	if reason != dlgMarker {
		t.Errorf("delegate lock reason = %q, want %q", reason, dlgMarker)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_LeavesForeignMarker: another session's
// marker on a managed lane is not this session's to release, so close leaves it
// exactly as it found it (spec §5 EvLeave, foreign column).
func TestUnlockOwnManagedWorktreeAtClose_LeavesForeignMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "theirs"})
	if err != nil {
		t.Fatalf("create foreign lane: %v", err)
	}
	foreignPath := first["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "ours"}); err != nil {
		t.Fatalf("create own lane: %v", err)
	}
	foreignMarker := worktree.FormatSessionMarker(r.s.id + "-other")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignMarker, foreignPath)

	r.s.unlockOwnManagedWorktreeAtClose()

	_, locked, reason := r.laneLocked(t, foreignPath)
	if !locked {
		t.Fatal("another session's lane was unlocked at close")
	}
	if reason != foreignMarker {
		t.Errorf("foreign lock reason = %q, want %q", reason, foreignMarker)
	}
}

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

// TestUnlockOwnManagedWorktreeAtClose_ListingFailsWarns: the registry read the
// sweep needs fails (git unavailable) — every lane stays locked and a warning
// names the lane directory it could not sweep, rather than the failure being
// swallowed.
func TestUnlockOwnManagedWorktreeAtClose_ListingFailsWarns(t *testing.T) {
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
		t.Error("own worktree wrongly unlocked when the registry read failed")
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, filepath.Dir(path), "unlocking own worktree", "failed") {
		t.Errorf("no warning about the failed close-unlock: %v", msgs)
	}
}

// TestUnlockOwnManagedWorktreeAtClose_UnlockFailsWarns: the sweep reads the
// registry but the release of one lane's own marker fails — that lane stays
// locked and a warning names it, rather than the failure being swallowed.
func TestUnlockOwnManagedWorktreeAtClose_UnlockFailsWarns(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.s.cfg.testOnly.worktreeGitRunner = func(runCtx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		run := gitRunner(runCtx, env)
		return func(args ...string) (string, error) {
			if len(args) > 1 && args[0] == "worktree" && args[1] == "unlock" {
				return "", errors.New("unlock refused")
			}
			return run(args...)
		}
	}

	r.s.unlockOwnManagedWorktreeAtClose()

	if _, locked, _ := r.laneLocked(t, path); !locked {
		t.Error("own worktree reported unlocked despite a failed unlock")
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

	// TRIPWIRE: this deadline stands in for an effectively-unlimited close
	// budget; the fake git runner below calls cancel() explicitly on the
	// first "worktree list" query, so this hour-long value is never actually
	// awaited and only bounds the test if that manual cancellation itself
	// regresses and never fires.
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
		if r.stableDisposalClosurePresent(t, id) {
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
		if r.stableDisposalClosurePresent(t, id) {
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
