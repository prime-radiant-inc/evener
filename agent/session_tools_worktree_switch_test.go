package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These are integration tests for the manage_worktree switch and exit arms
// (spec §4). They reuse wtRepo/wtGit from
// session_tools_worktree_create_test.go.
//
// This file is MIXED across the two lane harnesses; see docs/testing.md for the
// rule. A test whose subject is serf's own decision-making — which refusal rung
// fires, its error text, the env-swap and restore-state bookkeeping, argument
// validation — runs on the scripted git boundary (scriptedLaneRepo, driven
// through wtRepo's shared operation helpers). These stay on real git because
// their subject IS git's observable behavior:
//
//   - the git-unavailable tests (TestWorktreeSwitch_*ErrorsWhenGitUnavailable,
//     TestLeaveCurrentWorktree_LockStateErrorPropagates,
//     TestWorktreeExit_LeaveCurrentErrorsWhenGitUnavailable) — the absence of a
//     git binary, which a scripted runner cannot express
//   - TestWorktreeSwitch_BetweenTwoManagedWorktrees,
//     TestWorktreeSwitch_ToCurrentIsNoOpLockKept,
//     TestWorktreeSwitch_ByPathInsideManagedDirGetsFullChoreographyLocked,
//     TestWorktreeCreateExitSwitch_RoundTripRelocks,
//     TestWorktreeExit_RestoresEnvClearsSavedEnvUnlocks,
//     TestWorktreeExit_RestoringIntoUnlockedManagedLaunchRootTakesLock — real
//     lock/unlock effects read back through real `--porcelain`
//   - TestWorktreeSwitch_ByPathSkipsMomentarilyAbsentPorcelainEntry — a real
//     prunable registry entry
//   - TestWorktreeSwitch_ByPathSymlinkedSpellingCanonicalizedAccept — git's own
//     canonical registry path vs a symlinked spelling
//   - TestWorktreeExit_RelockLockCommandFailsOnPermissionDenied — git's own
//     failure writing its "locked" marker file

// managedPath returns the path a managed worktree named name would live at
// for canonicalMain, matching worktreeRootFor's derivation (stateDir set).
func (r *wtRepo) managedPath(t *testing.T, canonicalMain, name string) string {
	return filepath.Join(r.stateDir, "worktrees", resolvedProjectID(t, r.s.currentEnv(), canonicalMain), name)
}

// canonicalMain returns the symlink-resolved main repo root.
func (r *wtRepo) canonicalMain(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(r.mainRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks main root: %v", err)
	}
	return root
}

// addSiblingWorktree creates a non-managed sibling worktree (never touched by
// manage_worktree) at a symlink-resolved temp path and returns its canonical
// path. Git canonicalizes worktree paths when it registers them (macOS
// t.TempDir() lands under /var, a symlink to /private/var), so the path must
// already be resolved here for later comparisons against switch results,
// WorkingDirectory(), or porcelain entries to line up — the same discipline
// canonicalMain applies to the main repo root.
func (r *wtRepo) addSiblingWorktree(t *testing.T, dirName, branch string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks sibling root: %v", err)
	}
	path := filepath.Join(root, dirName)
	wtGit(t, r.mainRoot, "worktree", "add", "-b", branch, path, r.head)
	return path
}

// switchOp drives the switch operation through the registered tool surface.
func (r *wtRepo) switchOp(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	full := map[string]any{"operation": "switch"}
	for k, v := range args {
		full[k] = v
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), full)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("switch result is %T, want map[string]any", out)
	}
	return m, nil
}

// exitOp drives the exit operation through the registered tool surface.
func (r *wtRepo) exitOp(t *testing.T) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "exit"})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("exit result is %T, want map[string]any", out)
	}
	return m, nil
}

// --- switch by name ---

// REAL git: every lock assertion here is read back out of git's own registry,
// and envInfo.GitBranch comes from a real git snapshot of the entered worktree.
func TestWorktreeSwitch_BetweenTwoManagedWorktrees(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepoWithConfig(t, SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			minimalSystemPrompt:         true,
			minimalWorktreeToolRegistry: true,
			noSyncJobStore:              true,
		},
	})
	resA, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := resA["path"].(string)

	if _, err := r.create(t, map[string]any{"name": "B"}); err != nil {
		t.Fatalf("create B: %v", err)
	}
	// create-away already unlocked A; sanity check before the switch under test.
	if r.porcelainEntry(t, pathA).Locked {
		t.Fatal("A unexpectedly still locked after create-away")
	}

	restoreBefore := r.s.worktreeRestoreEnv
	if restoreBefore == nil || restoreBefore.WorkingDirectory() != r.mainRoot {
		t.Fatalf("saved restore env before switch = %v, want %s", restoreBefore, r.mainRoot)
	}

	out, err := r.switchOp(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("switch to A: %v", err)
	}
	if out["path"] != pathA {
		t.Errorf("switch result path = %v, want %s", out["path"], pathA)
	}
	if out["branch"] != "A" {
		t.Errorf("switch result branch = %v, want A", out["branch"])
	}

	// Lock moved: A locked with our marker, B (the worktree we left) unlocked.
	eA := r.porcelainEntry(t, pathA)
	if !eA.Locked || eA.LockReason != worktree.FormatSessionMarker(r.s.id) {
		t.Errorf("A lock = (%v,%q), want locked with own marker", eA.Locked, eA.LockReason)
	}

	// envInfo refreshed: reflects branch A.
	if r.s.envInfo.GitBranch != "A" {
		t.Errorf("envInfo.GitBranch = %q, want A", r.s.envInfo.GitBranch)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != pathA {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, pathA)
	}

	// The saved restore env (from the very first create) is untouched by the
	// intermediate switch — still the original main root, not B.
	restoreAfter := r.s.worktreeRestoreEnv
	if restoreAfter == nil || restoreAfter.WorkingDirectory() != r.mainRoot {
		t.Fatalf("saved restore env after switch = %v, want unchanged at %s", restoreAfter, r.mainRoot)
	}
}

// REAL git: "the lock did not change" is a claim about git's registry entry
// before and after, read through real `--porcelain`.
func TestWorktreeSwitch_ToCurrentIsNoOpLockKept(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := res["path"].(string)
	before := r.porcelainEntry(t, pathA)
	if !before.Locked {
		t.Fatal("A should be locked right after create")
	}

	out, err := r.switchOp(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("switch to current A: %v", err)
	}
	if out["status"] != "unchanged" {
		t.Errorf("status = %v, want unchanged for a no-op switch", out["status"])
	}

	after := r.porcelainEntry(t, pathA)
	if !after.Locked || after.LockReason != before.LockReason {
		t.Errorf("lock changed on a no-op switch: before=(%v,%q) after=(%v,%q)",
			before.Locked, before.LockReason, after.Locked, after.LockReason)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != pathA {
		t.Errorf("currentEnv WorkingDirectory = %q, want unchanged %q", got, pathA)
	}
}

func TestWorktreeSwitch_ToCurrentOutOfBandForeignMarkerRefuses(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	res, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := res["path"].(string)

	// Out-of-band: replace A's own-session lock with a foreign marker while
	// the session still believes it occupies A (simulating another tool or
	// session interfering between create and this switch). Decide's
	// EvEnterCurrent row treats this as unreachable in well-behaved code and
	// fails safe — refuse rather than silently no-op.
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000002")
	sr.setLaneLock(t, pathA, foreignReason)

	_, err = r.switchOp(t, map[string]any{"name": "A"})
	if err == nil {
		t.Fatal("expected switch-to-current to refuse when the lock is no longer this session's own marker")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error must name the foreign lock reason %q, got: %v", foreignReason, err)
	}

	// Refusal must not have mutated the env or the lock.
	if got := r.s.currentEnv().WorkingDirectory(); got != pathA {
		t.Errorf("currentEnv WorkingDirectory = %q after refused switch, want unchanged %q", got, pathA)
	}
	if got := sr.laneLockReason(t, pathA); got != foreignReason {
		t.Errorf("lock mutated by refused switch: got %q, want unchanged %q", got, foreignReason)
	}
}

func TestWorktreeSwitch_ForeignLockedTargetRefusesNamingReason(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	res, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := res["path"].(string)

	// Leave A (unlocks it), then simulate another session claiming it.
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000000")
	sr.setLaneLock(t, pathA, foreignReason)

	_, err = r.switchOp(t, map[string]any{"name": "A"})
	if err == nil {
		t.Fatal("expected switch to a foreign-locked target to be refused")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error must name the foreign lock reason %q, got: %v", foreignReason, err)
	}
	// Refusal must not have entered the worktree.
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("currentEnv WorkingDirectory = %q after refused switch, want unchanged %q", got, r.mainRoot)
	}
	// The foreign lock must survive untouched.
	if got := sr.laneLockReason(t, pathA); got != foreignReason {
		t.Errorf("foreign lock mutated: got %q, want unchanged %q", got, foreignReason)
	}
}

func TestWorktreeSwitch_RejectsInvalidName(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	_, err := r.switchOp(t, map[string]any{"name": "has space"})
	if err == nil {
		t.Fatal("expected switch to reject an invalid name")
	}
}

func TestWorktreeSwitch_NonexistentNameErrors(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	_, err := r.switchOp(t, map[string]any{"name": "never-created"})
	if err == nil {
		t.Fatal("expected switch to a non-existent managed worktree to fail")
	}
}

func TestWorktreeSwitch_RequiresExactlyOneOfNameOrPath(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	if _, err := r.switchOp(t, map[string]any{}); err == nil {
		t.Error("expected an error when neither name nor path is given")
	}
	if _, err := r.switchOp(t, map[string]any{"name": "A", "path": "/tmp/x"}); err == nil {
		t.Error("expected an error when both name and path are given")
	}
}

// TestWorktreeSwitch_TargetLockInspectionErrorsWhenGitUnavailable covers
// worktreeEnterManaged's own lockStateOf error branch (distinct from the
// same-target no-op path below): switching to a DIFFERENT managed worktree
// needs to inspect the TARGET's lock via a real git subprocess, which fails
// cleanly (not a panic, not a silent success) when git is hidden entirely.
//
// REAL git: the subject is the ABSENCE of a git binary. A scripted runner would
// answer every command, leaving nothing missing to prove anything about.
func TestWorktreeSwitch_TargetLockInspectionErrorsWhenGitUnavailable(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "A"}); err != nil {
		t.Fatalf("create A: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}

	_, err := r.switchOp(t, map[string]any{"name": "A"})
	if err == nil || !strings.Contains(err.Error(), "inspecting the target lock") {
		t.Fatalf("switch to A with git hidden: err = %v, want the target-lock-inspection error", err)
	}
}

// TestWorktreeSwitch_CurrentLockInspectionErrorsWhenGitUnavailable covers
// switchToCurrentNoOp's own lockStateOf error branch: switching to the
// worktree the session ALREADY occupies still needs to re-verify the lock
// (spec §4 switch step 1), which fails the same clean way with git hidden.
//
// REAL git: same reason as above — the missing binary IS the fixture.
func TestWorktreeSwitch_CurrentLockInspectionErrorsWhenGitUnavailable(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "A"}); err != nil {
		t.Fatalf("create A: %v", err)
	}
	// The session is now inside A already.

	t.Setenv("PATH", t.TempDir())
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}

	_, err := r.switchOp(t, map[string]any{"name": "A"})
	if err == nil || !strings.Contains(err.Error(), "inspecting the current worktree lock") {
		t.Fatalf("switch to the already-occupied A with git hidden: err = %v, want the current-lock-inspection error", err)
	}
}

// TestLeaveCurrentWorktree_LockStateErrorPropagates is a direct white-box
// test of leaveCurrentWorktree's own lockStateOf error branch, reached
// (in the tool surface) only as a sub-step of create-away/switch-away/exit —
// this exercises it in isolation with git hidden after a real occupied
// worktree is already in place.
//
// REAL git: the missing binary IS the fixture, and the runner under test is the
// production gitRunner rather than the session's injectable seam.
func TestLeaveCurrentWorktree_LockStateErrorPropagates(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "A"}); err != nil {
		t.Fatalf("create A: %v", err)
	}
	mainRoot := r.canonicalMain(t)
	controlEnv := r.s.currentEnv().(*execenv.LocalExecutionEnvironment).WithWorkingDirectory(mainRoot)
	run := gitRunner(context.Background(), controlEnv)

	t.Setenv("PATH", t.TempDir())
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}

	err := r.s.leaveCurrentWorktree(run)
	if err == nil || !strings.Contains(err.Error(), "inspecting the current worktree lock") {
		t.Fatalf("leaveCurrentWorktree with git hidden: err = %v, want the lock-inspection error", err)
	}
}

// TestWorktreeControlRun_NonLocalEnvErrors is a direct white-box test of
// worktreeControlRun's own local-execution-environment guard (the same
// underlying check as TestWorktreeControlEnv_NonLocalEnvErrors, but through
// this thin wrapper specifically).
func TestWorktreeControlRun_NonLocalEnvErrors(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	r.s.mu.Lock()
	r.s.env = &timeoutEnv{wd: r.mainRoot}
	r.s.mu.Unlock()

	_, err := r.s.worktreeControlRun(context.Background(), r.mainRoot)
	if err == nil || !strings.Contains(err.Error(), "local execution environment") {
		t.Fatalf("worktreeControlRun with a non-local env: err = %v, want the local-execution-environment error", err)
	}
}

// --- switch by path ---

// TestWorktreeSwitchByPath_NotInGitRepo covers worktreeSwitchByPath's own
// "not in a git repository" guard (st.mainRepoRoot == ""), independent of
// worktreeSwitchByName's identical-shaped guard.
func TestWorktreeSwitchByPath_NotInGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // not a git repo at all
	s := newSession(t, withDir(dir))
	_, err := s.worktreeSwitchByPath(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("switch by path outside a git repo: err = %v, want the not-in-a-git-repository error", err)
	}
}

// TestWorktreeSwitch_ByPathNonexistentPathErrors covers step 1's
// EvalSymlinks failure when the argument path does not exist at all.
func TestWorktreeSwitch_ByPathNonexistentPathErrors(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	ghost := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := r.switchOp(t, map[string]any{"path": ghost})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("switch by path to a nonexistent path: err = %v, want a does-not-exist error", err)
	}
}

// TestWorktreeSwitch_ByPathListWorktreesErrorsWhenGitUnavailable covers step
// 1's `git worktree list --porcelain` error branch.
//
// REAL git: the missing binary IS the fixture.
func TestWorktreeSwitch_ByPathListWorktreesErrorsWhenGitUnavailable(t *testing.T) {
	r := newWorktreeRepo(t)
	t.Setenv("PATH", t.TempDir())
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}
	_, err := r.switchOp(t, map[string]any{"path": r.mainRoot})
	if err == nil || !strings.Contains(err.Error(), "listing worktrees") {
		t.Fatalf("switch by path with git hidden: err = %v, want the listing-worktrees error", err)
	}
}

// TestWorktreeSwitch_ByPathSkipsMomentarilyAbsentPorcelainEntry covers step
// 1's tolerance for a registered worktree whose directory is momentarily
// gone (a real "prunable" entry: git's registry still lists it, but its
// EvalSymlinks fails) — the scan must skip it rather than error out, so a
// LATER, resolvable entry (the target we actually asked for) is still
// found.
//
// REAL git: the fixture is a genuinely stale registry entry — git still lists a
// worktree whose directory was deleted behind its back.
func TestWorktreeSwitch_ByPathSkipsMomentarilyAbsentPorcelainEntry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	// A sibling worktree whose directory is deleted out-of-band (not via
	// `git worktree remove`), leaving a stale, unresolvable porcelain entry.
	ghostRoot := t.TempDir()
	ghostPath := filepath.Join(ghostRoot, "ghost")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "ghost-branch", ghostPath, r.head)
	if err := os.RemoveAll(ghostPath); err != nil {
		t.Fatalf("remove ghost worktree dir: %v", err)
	}

	// A second, real sibling that we actually switch to — the scan must not
	// abort on the ghost entry before reaching this one.
	siblingPath := r.addSiblingWorktree(t, "sibling", "sibling-branch")

	out, err := r.switchOp(t, map[string]any{"path": siblingPath})
	if err != nil {
		t.Fatalf("switch by path past a ghost entry: %v", err)
	}
	if out["path"] != siblingPath {
		t.Errorf("switch result path = %v, want %s", out["path"], siblingPath)
	}
}

// REAL git: the fixture is a hand-made sibling worktree registered by real
// `worktree add` outside the managed dir, and the claim is that its real
// registry entry is never locked.
func TestWorktreeSwitch_ByPathSiblingManualWorktreeNoLockMutation(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	// A manually created worktree OUTSIDE the managed directory (as a human's
	// hand-made sibling lane would be), never touched by manage_worktree.
	siblingPath := r.addSiblingWorktree(t, "sibling", "sibling-branch")

	out, err := r.switchOp(t, map[string]any{"path": siblingPath})
	if err != nil {
		t.Fatalf("switch by path to sibling: %v", err)
	}
	if out["branch"] != "sibling-branch" {
		t.Errorf("branch = %v, want sibling-branch", out["branch"])
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != siblingPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, siblingPath)
	}
	// No lock mutation: serf never locks worktrees it doesn't manage.
	e := r.porcelainEntry(t, siblingPath)
	if e.Locked {
		t.Errorf("sibling worktree got locked; by-path switch to a non-managed worktree must not mutate locks")
	}
}

// REAL git: same hand-made sibling fixture, same real "never locked" claim.
func TestWorktreeSwitch_ByPathToCurrentNonManagedUnlockedNoOps(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	// A manually created worktree OUTSIDE the managed directory, never locked
	// by serf (spec §4 switch by-path step 3: "no lock choreography — serf
	// does not mutate lock state on worktrees it does not manage"). There is
	// no lock decision to adjudicate on this site at all, so a redundant
	// switch back to it is a plain path-compare no-op, not a run through the
	// EvEnterCurrent lock-state gate (that gate is for the managed by-name
	// site, which does have a lock to protect).
	siblingPath := r.addSiblingWorktree(t, "sibling", "sibling-branch")

	if _, err := r.switchOp(t, map[string]any{"path": siblingPath}); err != nil {
		t.Fatalf("switch by path to sibling: %v", err)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != siblingPath {
		t.Fatalf("currentEnv WorkingDirectory = %q after first switch, want %q", got, siblingPath)
	}

	out, err := r.switchOp(t, map[string]any{"path": siblingPath})
	if err != nil {
		t.Fatalf("redundant switch-to-current on a non-managed worktree should no-op, not refuse: %v", err)
	}
	if out["status"] != "unchanged" {
		t.Errorf("status = %v, want unchanged for a no-op switch", out["status"])
	}

	// No-op must not have mutated the env or taken any lock.
	if got := r.s.currentEnv().WorkingDirectory(); got != siblingPath {
		t.Errorf("currentEnv WorkingDirectory = %q after no-op switch, want unchanged %q", got, siblingPath)
	}
	if r.porcelainEntry(t, siblingPath).Locked {
		t.Error("no-op switch-to-current must not have locked the non-managed sibling")
	}
}

// REAL git: the accepted match is between a symlinked spelling and the CANONICAL
// path git itself recorded when it registered the worktree.
func TestWorktreeSwitch_ByPathSymlinkedSpellingCanonicalizedAccept(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	pathLane := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// A symlink elsewhere spelling the same real directory differently.
	linkDir := t.TempDir()
	linkPath := filepath.Join(linkDir, "lane-link")
	if err := os.Symlink(pathLane, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	out, err := r.switchOp(t, map[string]any{"path": linkPath})
	if err != nil {
		t.Fatalf("switch by symlinked path: %v", err)
	}
	if out["path"] != pathLane {
		t.Errorf("switch result path = %v, want canonicalized %s", out["path"], pathLane)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != pathLane {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, pathLane)
	}
}

func TestWorktreeSwitch_UnregisteredPathRejected(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	stray := t.TempDir()

	_, err := r.switchOp(t, map[string]any{"path": stray})
	if err == nil {
		t.Fatal("expected switch to an unregistered path to be rejected")
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("currentEnv WorkingDirectory = %q after rejected switch, want unchanged %q", got, r.mainRoot)
	}
}

// REAL git: the whole claim is that the by-path reroute really TAKES the lock in
// git's registry, read back through real `--porcelain`.
func TestWorktreeSwitch_ByPathInsideManagedDirGetsFullChoreographyLocked(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	pathLane := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if r.porcelainEntry(t, pathLane).Locked {
		t.Fatal("lane should be unlocked after exit")
	}

	out, err := r.switchOp(t, map[string]any{"path": pathLane})
	if err != nil {
		t.Fatalf("switch by path resolving inside managed dir: %v", err)
	}
	if out["path"] != pathLane {
		t.Errorf("switch result path = %v, want %s", out["path"], pathLane)
	}
	e := r.porcelainEntry(t, pathLane)
	if !e.Locked || e.LockReason != worktree.FormatSessionMarker(r.s.id) {
		t.Errorf("by-path switch into the managed dir must take the lock; got (%v,%q)", e.Locked, e.LockReason)
	}
}

// --- exit ---

// TestWorktreeExit_LeaveCurrentErrorsWhenGitUnavailable covers worktreeExit
// step 2's own leaveCurrentWorktree error-propagation site (distinct from
// TestLeaveCurrentWorktree_LockStateErrorPropagates' direct white-box test
// of the callee itself): hiding git entirely fails the VERY FIRST git call
// exit makes, so the error surfaces from this call site specifically.
//
// REAL git: the missing binary IS the fixture.
func TestWorktreeExit_LeaveCurrentErrorsWhenGitUnavailable(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create lane: %v", err)
	}
	restore := hideGitInRepo(t, r.mainRoot)
	defer restore()

	_, err := r.exitOp(t)
	if err == nil || !strings.Contains(err.Error(), "inspecting the current worktree lock") {
		t.Fatalf("exit with git hidden: err = %v, want the current-worktree-lock-inspection error", err)
	}
}

// TestWorktreeExit_RelockLockCommandFailsOnPermissionDenied covers step 4's
// relockRestoreTarget->ActLock `git worktree lock` failure branch: the
// restore root (launch) is genuinely unlocked, so Decide resolves to
// ActLock, but the lock command itself fails because its internal
// .git/worktrees/<id> directory has been made read-only (a real permission
// error, not a scripted one — git's own lock/unlock write a "locked" marker
// file there).
//
// REAL git: the failure under test is git's own, writing its lock marker into a
// directory it cannot write.
func TestWorktreeExit_RelockLockCommandFailsOnPermissionDenied(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	_, r2, launchPath, _ := wtLaunchSession(t, r)

	if r2.porcelainEntry(t, launchPath).Locked {
		t.Fatal("launch worktree should still be unlocked before exit")
	}
	internalDir := worktreeInternalDir(t, r.mainRoot, launchPath)
	chmodReadOnly(t, internalDir)

	_, err := r2.exitOp(t)
	if err == nil || !strings.Contains(err.Error(), "locking the restore target") {
		t.Fatalf("exit with the lock internal dir read-only: err = %v, want the locking-the-restore-target error", err)
	}
}

// REAL git: exit's non-destructiveness is proven against real state — the branch
// really still exists in the ref store, and the lane's real registry entry is
// really unlocked.
func TestWorktreeExit_RestoresEnvClearsSavedEnvUnlocks(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	pathLane := res["path"].(string)
	canonicalMain := r.canonicalMain(t)

	out, err := r.exitOp(t)
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	if out["restored_root"] != r.mainRoot {
		t.Errorf("restored_root = %v, want %s", out["restored_root"], r.mainRoot)
	}
	if out["left_path"] != pathLane {
		t.Errorf("left_path = %v, want %s", out["left_path"], pathLane)
	}

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, r.mainRoot)
	}
	if r.s.worktreeRestoreEnv != nil {
		t.Errorf("saved restore env not cleared after exit")
	}

	// Worktree + branch + sidecar stay intact.
	if _, statErr := os.Stat(filepath.Join(pathLane, ".git")); statErr != nil {
		t.Errorf("worktree removed by exit: %v", statErr)
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch removed by exit")
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, canonicalMain), "lane"); scErr != nil {
		t.Errorf("sidecar removed by exit: %v", scErr)
	}

	// Unlocked.
	if r.porcelainEntry(t, pathLane).Locked {
		t.Error("lane still locked after exit")
	}
}

func TestWorktreeExit_OutsideWorktreeErrorsNoSideEffects(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t).wt()
	_, err := r.exitOp(t)
	if err == nil {
		t.Fatal("expected exit outside a worktree to error")
	}
	if !strings.Contains(err.Error(), "not in a worktree") {
		t.Errorf("error = %v, want it to say 'not in a worktree'", err)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("currentEnv WorkingDirectory = %q after a no-op exit error, want unchanged %q", got, r.mainRoot)
	}
	if r.s.worktreeRestoreEnv != nil {
		t.Error("exit outside a worktree must not touch the saved restore env")
	}
}

func TestWorktreeExit_RestoringIntoManagedLaunchRootIdempotentRelockForeignWarns(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	launch, launchPath, pathWork := sr.launchSession(t)
	s2, r2 := launch.s, launch.wt()

	// Simulate another session/tool claiming the launch worktree while s2 was
	// away in "work".
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000001")
	sr.setLaneLock(t, launchPath, foreignReason)

	out, err := r2.exitOp(t)
	if err != nil {
		t.Fatalf("exit must warn-and-continue on a foreign-locked restore target, not refuse: %v", err)
	}
	if out["restored_root"] != launchPath {
		t.Errorf("restored_root = %v, want %s", out["restored_root"], launchPath)
	}
	warning, _ := out["warning"].(string)
	if warning == "" {
		t.Fatal("expected a surfaced warning for a foreign-locked restore target")
	}
	if !strings.Contains(warning, foreignReason) {
		t.Errorf("warning = %q, want it to name the foreign reason %q", warning, foreignReason)
	}

	// The session still lands there despite the foreign lock (a restore cannot
	// be refused).
	if got := s2.currentEnv().WorkingDirectory(); got != launchPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, launchPath)
	}
	// The foreign lock on the launch worktree is left untouched (co-occupy, not
	// a forced takeover).
	if got := sr.laneLockReason(t, launchPath); got != foreignReason {
		t.Errorf("launch worktree lock = %q, want untouched %q", got, foreignReason)
	}
	// The worktree left ("work") was unlocked on the way out.
	if got := sr.laneLockReason(t, pathWork); got != "" {
		t.Errorf("work worktree still locked after exit: %q", got)
	}
}

// wtLaunchSession builds a managed "launch" worktree by hand (as if a prior
// create/switch had put it there), then roots a fresh session at it —
// simulating "the session was launched inside a managed worktree", and from
// inside it creates a "work" lane so the launch worktree is saved as the
// restore env. Shared by the exit relock-branch tests below and the
// foreign-warn test.
func wtLaunchSession(t *testing.T, r *wtRepo) (s2 *Session, r2 *wtRepo, launchPath, workPath string) {
	t.Helper()
	canonicalMain := r.canonicalMain(t)
	launchPath = r.managedPath(t, canonicalMain, "launch")

	if err := os.MkdirAll(filepath.Dir(launchPath), 0o755); err != nil {
		t.Fatalf("mkdir launch parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "launch", launchPath, r.head)

	s2 = newSession(t, withDir(launchPath), withConfig(worktreeTestSessionConfig()))
	s2.stateDir = r.stateDir
	r2 = &wtRepo{s: s2, mainRoot: r.mainRoot, stateDir: r.stateDir, head: r.head}

	resWork, err := r2.create(t, map[string]any{"name": "work"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	return s2, r2, launchPath, resWork["path"].(string)
}

// REAL git: the assertion is that the restore really TAKES a lock in git's
// registry on a lane git itself reports as unlocked.
func TestWorktreeExit_RestoringIntoUnlockedManagedLaunchRootTakesLock(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	s2, r2, launchPath, _ := wtLaunchSession(t, r)

	if r2.porcelainEntry(t, launchPath).Locked {
		t.Fatal("launch worktree should still be unlocked before exit")
	}

	out, err := r2.exitOp(t)
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	if out["restored_root"] != launchPath {
		t.Errorf("restored_root = %v, want %s", out["restored_root"], launchPath)
	}
	if warning, _ := out["warning"].(string); warning != "" {
		t.Errorf("unexpected warning restoring into an unlocked managed launch root: %q", warning)
	}

	e := r2.porcelainEntry(t, launchPath)
	want := worktree.FormatSessionMarker(s2.id)
	if !e.Locked || e.LockReason != want {
		t.Errorf("launch worktree lock after exit = (%v,%q), want (%v,%q)", e.Locked, e.LockReason, true, want)
	}
}

func TestWorktreeExit_RestoringIntoOwnMarkerManagedLaunchRootAdopts(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	launch, launchPath, _ := sr.launchSession(t)
	s2, r2 := launch.s, launch.wt()

	// Crash-residue simulation: the launch worktree already carries this
	// session's own marker (as if a prior lock survived a crash) before the
	// restore lands there again.
	ownReason := worktree.FormatSessionMarker(s2.id)
	sr.setLaneLock(t, launchPath, ownReason)

	out, err := r2.exitOp(t)
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	if out["restored_root"] != launchPath {
		t.Errorf("restored_root = %v, want %s", out["restored_root"], launchPath)
	}
	if warning, _ := out["warning"].(string); warning != "" {
		t.Errorf("unexpected warning adopting an own-marker restore target: %q", warning)
	}

	if got := sr.laneLockReason(t, launchPath); got != ownReason {
		t.Errorf("launch worktree lock after exit = %q, want unchanged %q", got, ownReason)
	}
}

// REAL git: the round trip's three lock states (locked, unlocked, relocked) are
// each read out of git's own registry.
func TestWorktreeCreateExitSwitch_RoundTripRelocks(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	resA, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := resA["path"].(string)
	if !r.porcelainEntry(t, pathA).Locked {
		t.Fatal("A should be locked right after create")
	}

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if r.porcelainEntry(t, pathA).Locked {
		t.Fatal("A should be unlocked after exit")
	}

	out, err := r.switchOp(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("switch back to A: %v", err)
	}
	if out["path"] != pathA {
		t.Errorf("switch result path = %v, want %s", out["path"], pathA)
	}

	e := r.porcelainEntry(t, pathA)
	if !e.Locked || e.LockReason != worktree.FormatSessionMarker(r.s.id) {
		t.Errorf("A relock = (%v,%q), want locked with own marker", e.Locked, e.LockReason)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != pathA {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, pathA)
	}
}
