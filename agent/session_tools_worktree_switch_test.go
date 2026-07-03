package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for the manage_worktree switch and exit
// arms (spec §4). They reuse wtRepo/wtGit from
// session_tools_worktree_create_test.go.

// managedPath returns the path a managed worktree named name would live at
// for canonicalMain, matching worktreeRootFor's derivation (stateDir set).
func (r *wtRepo) managedPath(canonicalMain, name string) string {
	return filepath.Join(r.stateDir, "worktrees", worktree.ProjectID(canonicalMain), name)
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

func TestWorktreeSwitch_BetweenTwoManagedWorktrees(t *testing.T) {
	r := newWorktreeRepo(t)
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

func TestWorktreeSwitch_ToCurrentIsNoOpLockKept(t *testing.T) {
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
	r := newWorktreeRepo(t)
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
	wtGit(t, r.mainRoot, "worktree", "unlock", pathA)
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000002")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, pathA)

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
	e := r.porcelainEntry(t, pathA)
	if !e.Locked || e.LockReason != foreignReason {
		t.Errorf("lock mutated by refused switch: got (%v,%q), want (%v,%q)", e.Locked, e.LockReason, true, foreignReason)
	}
}

func TestWorktreeSwitch_ForeignLockedTargetRefusesNamingReason(t *testing.T) {
	r := newWorktreeRepo(t)
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
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, pathA)

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
	e := r.porcelainEntry(t, pathA)
	if !e.Locked || e.LockReason != foreignReason {
		t.Errorf("foreign lock mutated: got (%v,%q), want (%v,%q)", e.Locked, e.LockReason, true, foreignReason)
	}
}

func TestWorktreeSwitch_RejectsInvalidName(t *testing.T) {
	r := newWorktreeRepo(t)
	_, err := r.switchOp(t, map[string]any{"name": "has space"})
	if err == nil {
		t.Fatal("expected switch to reject an invalid name")
	}
}

func TestWorktreeSwitch_NonexistentNameErrors(t *testing.T) {
	r := newWorktreeRepo(t)
	_, err := r.switchOp(t, map[string]any{"name": "never-created"})
	if err == nil {
		t.Fatal("expected switch to a non-existent managed worktree to fail")
	}
}

func TestWorktreeSwitch_RequiresExactlyOneOfNameOrPath(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.switchOp(t, map[string]any{}); err == nil {
		t.Error("expected an error when neither name nor path is given")
	}
	if _, err := r.switchOp(t, map[string]any{"name": "A", "path": "/tmp/x"}); err == nil {
		t.Error("expected an error when both name and path are given")
	}
}

// --- switch by path ---

func TestWorktreeSwitch_ByPathSiblingManualWorktreeNoLockMutation(t *testing.T) {
	r := newWorktreeRepo(t)
	// A manually created worktree OUTSIDE the managed directory (as a human's
	// hand-made sibling lane would be), never touched by manage_worktree.
	siblingRoot := t.TempDir()
	siblingPath := filepath.Join(siblingRoot, "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling-branch", siblingPath, r.head)

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

func TestWorktreeSwitch_ByPathToCurrentNonManagedUnlockedRefuses(t *testing.T) {
	r := newWorktreeRepo(t)
	// A manually created worktree OUTSIDE the managed directory, never locked
	// by serf (spec §4 switch by-path step 3: "no lock choreography"). A
	// redundant switch back to it has nothing to corroborate the claimed
	// occupancy through the lock state machine, so EvEnterCurrent's ordinary
	// (unlocked) outcome refuses rather than silently no-opping — the same
	// gate the managed by-name case goes through.
	siblingRoot := t.TempDir()
	siblingPath := filepath.Join(siblingRoot, "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling-branch", siblingPath, r.head)

	if _, err := r.switchOp(t, map[string]any{"path": siblingPath}); err != nil {
		t.Fatalf("switch by path to sibling: %v", err)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != siblingPath {
		t.Fatalf("currentEnv WorkingDirectory = %q after first switch, want %q", got, siblingPath)
	}

	_, err := r.switchOp(t, map[string]any{"path": siblingPath})
	if err == nil {
		t.Fatal("expected a redundant switch-to-current on an unlocked non-managed worktree to refuse")
	}

	// Refusal must not have mutated the env or taken any lock.
	if got := r.s.currentEnv().WorkingDirectory(); got != siblingPath {
		t.Errorf("currentEnv WorkingDirectory = %q after refused switch, want unchanged %q", got, siblingPath)
	}
	if r.porcelainEntry(t, siblingPath).Locked {
		t.Error("refused switch-to-current must not have locked the non-managed sibling")
	}
}

func TestWorktreeSwitch_ByPathSymlinkedSpellingCanonicalizedAccept(t *testing.T) {
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
	r := newWorktreeRepo(t)
	stray := t.TempDir()

	_, err := r.switchOp(t, map[string]any{"path": stray})
	if err == nil {
		t.Fatal("expected switch to an unregistered path to be rejected")
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("currentEnv WorkingDirectory = %q after rejected switch, want unchanged %q", got, r.mainRoot)
	}
}

func TestWorktreeSwitch_ByPathInsideManagedDirGetsFullChoreographyLocked(t *testing.T) {
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

func TestWorktreeExit_RestoresEnvClearsSavedEnvUnlocks(t *testing.T) {
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
	if _, scErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "lane"); scErr != nil {
		t.Errorf("sidecar removed by exit: %v", scErr)
	}

	// Unlocked.
	if r.porcelainEntry(t, pathLane).Locked {
		t.Error("lane still locked after exit")
	}
}

func TestWorktreeExit_OutsideWorktreeErrorsNoSideEffects(t *testing.T) {
	r := newWorktreeRepo(t)
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
	r := newWorktreeRepo(t)
	s2, r2, launchPath, pathWork := wtLaunchSession(t, r)

	// Simulate another session/tool claiming the launch worktree while s2 was
	// away in "work".
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000001")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, launchPath)

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
	e := r2.porcelainEntry(t, launchPath)
	if !e.Locked || e.LockReason != foreignReason {
		t.Errorf("launch worktree lock = (%v,%q), want untouched (%v,%q)", e.Locked, e.LockReason, true, foreignReason)
	}
	// The worktree left ("work") was unlocked on the way out.
	if r2.porcelainEntry(t, pathWork).Locked {
		t.Error("work worktree still locked after exit")
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
	launchPath = r.managedPath(canonicalMain, "launch")

	if err := os.MkdirAll(filepath.Dir(launchPath), 0o755); err != nil {
		t.Fatalf("mkdir launch parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "launch", launchPath, r.head)

	s2 = newSession(t, withDir(launchPath))
	s2.stateDir = r.stateDir
	r2 = &wtRepo{s: s2, mainRoot: r.mainRoot, stateDir: r.stateDir, head: r.head}

	resWork, err := r2.create(t, map[string]any{"name": "work"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	return s2, r2, launchPath, resWork["path"].(string)
}

func TestWorktreeExit_RestoringIntoUnlockedManagedLaunchRootTakesLock(t *testing.T) {
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
	r := newWorktreeRepo(t)
	s2, r2, launchPath, _ := wtLaunchSession(t, r)

	// Crash-residue simulation: the launch worktree already carries this
	// session's own marker (as if a prior lock survived a crash) before the
	// restore lands there again.
	ownReason := worktree.FormatSessionMarker(s2.id)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", ownReason, launchPath)

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

	e := r2.porcelainEntry(t, launchPath)
	if !e.Locked || e.LockReason != ownReason {
		t.Errorf("launch worktree lock after exit = (%v,%q), want unchanged (%v,%q)", e.Locked, e.LockReason, true, ownReason)
	}
}

func TestWorktreeCreateExitSwitch_RoundTripRelocks(t *testing.T) {
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
