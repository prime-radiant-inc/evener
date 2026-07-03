package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for the manage_worktree remove arm
// (spec §5 remove). They reuse wtRepo/wtGit/wtLaunchSession from
// session_tools_worktree_create_test.go and session_tools_worktree_switch_test.go.

// removeOp drives the remove operation through the registered tool surface.
func (r *wtRepo) removeOp(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	full := map[string]any{"operation": "remove"}
	for k, v := range args {
		full[k] = v
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), full)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("remove result is %T, want map[string]any", out)
	}
	return m, nil
}

// secondSession builds a second session rooted at r.mainRoot sharing the same
// worktreeRoot (stateDir) but with a different session id, for cross-session
// guard tests.
func (r *wtRepo) secondSession(t *testing.T) *wtRepo {
	t.Helper()
	s2 := newSession(t, withDir(r.mainRoot))
	s2.stateDir = r.stateDir
	return &wtRepo{s: s2, mainRoot: r.mainRoot, stateDir: r.stateDir, head: r.head}
}

// gitArgvRecordingShim installs a PATH-shimmed `git` that appends its full
// argument line to a log file before forwarding to the real git, so a test
// can assert exactly which git subcommands ran (spec §5 remove step 9: `-d`
// must never be invoked). It sets PATH for the duration of the test via
// t.Setenv, so it must be installed before the code under test runs.
func gitArgvRecordingShim(t *testing.T) (logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	logPath = filepath.Join(shimDir, "argv.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// --- 1: clean remove ---

func TestWorktreeRemove_CleanRemove(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	out, err := r.removeOp(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["path"] != path {
		t.Errorf("remove path = %v, want %s", out["path"], path)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (delete_branch not requested)", out["branch_deleted"])
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir survived remove: err=%v", statErr)
	}
	out2 := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	for _, e := range worktree.ParsePorcelain(out2) {
		if filepath.Clean(e.Path) == filepath.Clean(path) {
			t.Errorf("git worktree list still shows removed worktree: %+v", e)
		}
	}

	// delete_branch was not requested: the branch survives, and the sidecar
	// stays (marked worktree_removed) per spec §5 remove step 10.
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch removed despite delete_branch not being requested")
	}
	canonicalMain := r.canonicalMain(t)
	sc, scErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "lane")
	if scErr != nil {
		t.Fatalf("read sidecar: %v", scErr)
	}
	if !sc.WorktreeRemoved {
		t.Error("sidecar worktree_removed not marked true")
	}
	if sc.TipSHAAtRemoval != r.head {
		t.Errorf("sidecar tip_sha_at_removal = %q, want %q (lane never advanced)", sc.TipSHAAtRemoval, r.head)
	}
}

// --- 2: dirty without force ---

func TestWorktreeRemove_DirtyWithoutForceErrorsListsFilesEnvUnchanged(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	before := r.s.currentEnv().WorkingDirectory()
	_, err = r.removeOp(t, map[string]any{"name": "lane"})
	if err == nil {
		t.Fatal("expected remove of a dirty worktree without force to error")
	}
	if !strings.Contains(err.Error(), "dirty.txt") {
		t.Errorf("error must list the offending file, got: %v", err)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != before {
		t.Errorf("env changed on a refused remove: got %q, want unchanged %q", got, before)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the refusal: %v", statErr)
	}
}

// --- 3: force removes dirty ---

func TestWorktreeRemove_ForceRemovesDirty(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	if _, err := r.removeOp(t, map[string]any{"name": "lane", "force": true}); err != nil {
		t.Fatalf("force remove: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir survived a forced remove: err=%v", statErr)
	}
}

// --- 4: delete_branch on a merged branch deletes with -D after the gate ---

func TestWorktreeRemove_DeleteBranchMergedDeletesAfterGate(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, path, "config", "user.email", "test@example.com")
	wtGit(t, path, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// Fast-forward main to lane's tip so the ancestry arm holds.
	wtGit(t, r.mainRoot, "merge", "--ff-only", "lane")

	out, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != true {
		t.Errorf("branch_deleted = %v, want true (lane is merged into main)", out["branch_deleted"])
	}
	if branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch survived a merged delete_branch remove")
	}
	canonicalMain := r.canonicalMain(t)
	if _, scErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "lane"); !os.IsNotExist(scErr) {
		t.Errorf("sidecar survived: err=%v", scErr)
	}
}

// --- 5: delete_branch on an unmerged branch refuses with evidence, keeps the branch and sidecar ---

func TestWorktreeRemove_DeleteBranchUnmergedRefusesEvidenceSidecarKept(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, path, "config", "user.email", "test@example.com")
	wtGit(t, path, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")
	laneTip := strings.TrimSpace(wtGit(t, path, "rev-parse", "HEAD"))

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// main is NOT advanced: lane is never merged.

	out, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (lane is unmerged)", out["branch_deleted"])
	}
	reason, _ := out["branch_kept_reason"].(string)
	if reason == "" {
		t.Fatal("expected branch_kept_reason evidence for an unmerged refusal")
	}
	if !strings.Contains(reason, "not merged") {
		t.Errorf("branch_kept_reason = %q, want it to say the branch is not merged", reason)
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch was deleted despite being unmerged")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir survived remove: err=%v", statErr)
	}

	canonicalMain := r.canonicalMain(t)
	sc, scErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "lane")
	if scErr != nil {
		t.Fatalf("sidecar deleted despite the unmerged refusal: %v", scErr)
	}
	if !sc.WorktreeRemoved {
		t.Error("sidecar worktree_removed not marked true")
	}
	if sc.TipSHAAtRemoval != laneTip {
		t.Errorf("sidecar tip_sha_at_removal = %q, want lane's tip %q", sc.TipSHAAtRemoval, laneTip)
	}
}

// --- 6: detached-HEAD-review fixture — serf's own gate refuses where `-d` would succeed ---

func TestWorktreeRemove_DetachedHeadReviewRefusesNeverInvokesLowercaseD(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "feature"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, path, "config", "user.email", "test@example.com")
	wtGit(t, path, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(path, "f.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	wtGit(t, path, "add", "f.txt")
	wtGit(t, path, "commit", "-m", "advance feature")
	featureTip := strings.TrimSpace(wtGit(t, path, "rev-parse", "HEAD"))

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// Detach the main checkout's HEAD directly at feature's tip. Under a
	// review-of-the-tip workflow this is exactly the scenario rev-6 review
	// caught: `git branch -d feature` run from here would see feature as
	// trivially merged into the current (detached) HEAD and succeed — but
	// main's actual branch ref never moved, so serf's own gate (which never
	// consults HEAD) must refuse.
	wtGit(t, r.mainRoot, "checkout", "--detach", featureTip)

	logPath := gitArgvRecordingShim(t)

	out, err := r.removeOp(t, map[string]any{"name": "feature", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (serf's gate must refuse despite `-d` would succeed here)", out["branch_deleted"])
	}
	if !branchExistsInRepo(t, r.mainRoot, "feature") {
		t.Error("branch was deleted despite the detached-HEAD-review trap")
	}

	log, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read shim log: %v", readErr)
	}
	logStr := string(log)
	if logStr == "" {
		t.Fatal("shim recorded no git invocations; the shim was not exercised")
	}
	for _, line := range strings.Split(strings.TrimSpace(logStr), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "-d" && i > 0 && fields[0] == "branch" {
				t.Fatalf("git branch -d was invoked (must always be -D, gated): %q", line)
			}
		}
	}
}

// --- 7: branch checked out elsewhere surfaces the checkout location ---

func TestWorktreeRemove_BranchCheckedOutElsewhereSurfacesLocation(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// A second, non-managed checkout of the SAME branch (--force bypasses
	// git's normal one-checkout-per-branch rule) — lane is unchanged, so
	// serf's merge gate passes trivially, and the only refusal is git's own
	// "branch checked out elsewhere" rule at the `branch -D` step.
	otherPath := filepath.Join(t.TempDir(), "other-checkout")
	wtGit(t, r.mainRoot, "worktree", "add", "--force", otherPath, "lane")

	out, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (checked out elsewhere)", out["branch_deleted"])
	}
	reason, _ := out["branch_kept_reason"].(string)
	if !strings.Contains(reason, otherPath) {
		t.Errorf("branch_kept_reason = %q, want it to name the checkout location %s", reason, otherPath)
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch was deleted despite being checked out elsewhere")
	}
}

// --- 8: foreign lock refuses regardless of force ---

func TestWorktreeRemove_ForeignLockRefusesRegardlessOfForce(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000003")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, path)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove of a foreign-locked target to be refused even with force")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error must name the foreign lock reason %q, got: %v", foreignReason, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the foreign-lock refusal: %v", statErr)
	}
	e := r.porcelainEntry(t, path)
	if !e.Locked || e.LockReason != foreignReason {
		t.Errorf("foreign lock mutated: got (%v,%q), want unchanged (%v,%q)", e.Locked, e.LockReason, true, foreignReason)
	}
}

// --- 9: own-marker crash residue auto-unlocks and proceeds ---

func TestWorktreeRemove_OwnMarkerCrashResidueAutoUnlocksAndProceeds(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// Simulate a crash that left this session's own lock behind on a
	// worktree it is not currently in.
	ownReason := worktree.FormatSessionMarker(r.s.id)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", ownReason, path)

	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived remove of its own crash-residue lock: err=%v", statErr)
	}
}

// --- 10: remove-current restores and applies the restore-land relock rule ---

func TestWorktreeRemove_RemoveCurrentRestoresAndRelocks(t *testing.T) {
	r := newWorktreeRepo(t)
	s2, r2, launchPath, pathWork := wtLaunchSession(t, r)

	if r2.porcelainEntry(t, launchPath).Locked {
		t.Fatal("launch worktree should still be unlocked before remove-current")
	}
	if !r2.porcelainEntry(t, pathWork).Locked {
		t.Fatal("work worktree should be locked (session is inside it)")
	}

	out, err := r2.removeOp(t, map[string]any{"name": "work"})
	if err != nil {
		t.Fatalf("remove-current: %v", err)
	}
	if out["path"] != pathWork {
		t.Errorf("remove path = %v, want %s", out["path"], pathWork)
	}
	if warning, _ := out["warning"].(string); warning != "" {
		t.Errorf("unexpected warning restoring into an unlocked managed launch root: %q", warning)
	}

	if got := s2.currentEnv().WorkingDirectory(); got != launchPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want restored to %q", got, launchPath)
	}
	if _, statErr := os.Stat(pathWork); !os.IsNotExist(statErr) {
		t.Errorf("work worktree survived remove-current: err=%v", statErr)
	}

	// The restore landed in a managed worktree (launch) that was unlocked —
	// the idempotent restore-land rule must have taken this session's lock on
	// it (spec §5 "Restores follow the same rule").
	e := r2.porcelainEntry(t, launchPath)
	want := worktree.FormatSessionMarker(s2.id)
	if !e.Locked || e.LockReason != want {
		t.Errorf("launch worktree lock after remove-current = (%v,%q), want (%v,%q)", e.Locked, e.LockReason, true, want)
	}
}

// --- 11: remove-current with no safe restore env refuses ---

func TestWorktreeRemove_RemoveCurrentNoSafeRestoreEnvRefuses(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	launchPath := r.managedPath(canonicalMain, "launch")
	if err := os.MkdirAll(filepath.Dir(launchPath), 0o755); err != nil {
		t.Fatalf("mkdir launch parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "launch", launchPath, r.head)

	// A session launched directly inside a managed worktree, having never
	// gone through create/switch — no restore env was ever saved. Give it a
	// real sidecar (as a genuine prior `create` would have) so the isolated
	// behavior under test is step 7's no-safe-restore-env refusal, not step
	// 5's unmanaged-provenance refusal.
	s2 := newSession(t, withDir(launchPath))
	s2.stateDir = r.stateDir
	r2 := &wtRepo{s: s2, mainRoot: r.mainRoot, stateDir: r.stateDir, head: r.head}
	metaDir := r2.metaDir(canonicalMain)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metaDir: %v", err)
	}
	sc := worktree.Sidecar{
		Name:           "launch",
		Branch:         "launch",
		BaseSHA:        r.head,
		OriginalRoot:   canonicalMain,
		CreatorSession: s2.id,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := worktree.WriteSidecarExcl(metaDir, "launch", sc); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	_, err := r2.removeOp(t, map[string]any{"name": "launch"})
	if err == nil {
		t.Fatal("expected remove-current with no safe restore env to be refused")
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Errorf("error should explain the missing restore env, got: %v", err)
	}
	if _, statErr := os.Stat(launchPath); statErr != nil {
		t.Errorf("launch worktree removed despite the refusal: %v", statErr)
	}
	if got := s2.currentEnv().WorkingDirectory(); got != launchPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want unchanged %q", got, launchPath)
	}
}

// --- 12: live-work guard, via the test-only stub seam ---

func TestWorktreeRemove_LiveWorkGuardRefusesViaStub(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// worktreeLiveWorkStub stands in for Task 20's background-shell-job
	// launch-workdir field (spec §5 remove step 4's "New plumbing"); this
	// test exercises the guard CALL that Task 15 wires into the remove flow,
	// not the real job-store scan.
	r.s.worktreeLiveWorkStub = func(p string) []string {
		if p == filepath.Clean(path) {
			return []string{"job_abc123 (shell, running)"}
		}
		return nil
	}

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the live-work guard")
	}
	if !strings.Contains(err.Error(), "job_abc123") {
		t.Errorf("error should surface the live-work evidence, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the live-work guard: %v", statErr)
	}

	// Clearing the stub lets the same remove proceed.
	r.s.worktreeLiveWorkStub = nil
	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove after clearing the stub: %v", err)
	}
}

// --- 13: cross-creator sidecar refuses without force ---

func TestWorktreeRemove_CrossCreatorSidecarRefusesWithoutForce(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "shared"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	r2 := r.secondSession(t)
	if r2.s.id == r.s.id {
		t.Fatal("secondSession returned the same session id")
	}

	_, err = r2.removeOp(t, map[string]any{"name": "shared"})
	if err == nil {
		t.Fatal("expected a cross-creator remove without force to be refused")
	}
	if !strings.Contains(err.Error(), r.s.id) {
		t.Errorf("error should name the creator session %q, got: %v", r.s.id, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the cross-creator refusal: %v", statErr)
	}

	if _, err := r2.removeOp(t, map[string]any{"name": "shared", "force": true}); err != nil {
		t.Fatalf("forced cross-creator remove: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived a forced cross-creator remove: err=%v", statErr)
	}
}
