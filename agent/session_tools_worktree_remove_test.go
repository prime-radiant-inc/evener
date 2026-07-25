package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/worktree"
)

// These are integration tests for the manage_worktree remove arm (spec §5
// remove). They reuse wtRepo/wtGit/wtLaunchSession from
// session_tools_worktree_create_test.go and session_tools_worktree_switch_test.go.
//
// This file is MIXED across the two lane harnesses; see docs/testing.md for the
// rule. A test whose subject is serf's own decision-making — which refusal rung
// fires, its error text, the restore-state bookkeeping, what serf wrote to its
// own sidecar — runs on the scripted git boundary (scriptedLaneRepo, driven
// through wtRepo's shared operation helpers). These stay on real git because
// their subject IS git's observable behavior:
//
//   - TestWorktreeRemove_BasicDispositionMatrix, ...ForceDoesNotDiscardUncommitted
//     — real dirty detection and git's own refusal to discard uncommitted work.
//     The scripted model reports every tree CLEAN, so a converted dirty gate
//     would invert silently while still passing.
//   - TestWorktreeRemove_DeleteBranchMergedDeletesAfterGate,
//     ...DeleteBranchUnmergedRefusesEvidenceSidecarKept,
//     ...DeleteBranchMergeTargetUnknownRefusesWithEvidence,
//     ...DetachedHeadReviewRefusesNeverInvokesLowercaseD — real ancestry. The
//     model's `merge-base --is-ancestor` always succeeds, so every lane reads as
//     merged and both verdicts of the gate would stop being distinguishable.
//   - TestWorktreeRemove_BranchCheckedOutElsewhereSurfacesLocation — git's own
//     one-checkout-per-branch rule and the location it reports
//   - TestWorktreeRemove_OwnMarkerCrashResidueAutoUnlocksAndProceeds — the proof
//     that the auto-unlock really happened is that git's `worktree remove`, which
//     refuses a locked entry, then succeeded. The model does not check locks on
//     remove, so a converted version would pass even if the unlock were skipped.
//   - TestWorktreeRemove_RemoveCurrentRestoresAndRelocks — real lock/unlock
//     effects read back through real `--porcelain`
//   - TestWorktreeRemove_DeleteSidecarFailsOnPermissionDenied,
//     ...CrashResidueUnlockFailsOnPermissionDenied,
//     ...RemoveCurrentUnlockBeforeRestoreFailsOnPermissionDenied — git's own
//     failure writing its lock marker into a read-only internal directory
//   - TestWorktreeRemove_TargetLockInspectionErrorsWhenGitUnavailable — the
//     absence of a git binary

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

func gitArgvRecordingRepoShim(t *testing.T, repoRoot string) (logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	logPath = filepath.Join(t.TempDir(), "argv.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	writeRepoGitShim(t, repoRoot, script)
	return logPath
}

// --- 1: clean remove ---

// TestWorktreeRemove_NotInGitRepo covers worktreeRemove's own "not in a git
// repository" guard.
func TestWorktreeRemove_NotInGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // not a git repo at all; no git boundary is reached
	s := newSession(t, withDir(dir))
	_, err := s.worktreeRemove(context.Background(), "x", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("remove outside a git repo: err = %v, want the not-in-a-git-repository error", err)
	}
}

// TestWorktreeRemove_TargetLockInspectionErrorsWhenGitUnavailable covers
// step 3's lockStateOf error branch for a non-current target.
//
// REAL git: the subject is the ABSENCE of a git binary; a scripted runner would
// answer every command, leaving nothing missing to prove anything about.
func TestWorktreeRemove_TargetLockInspectionErrorsWhenGitUnavailable(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	r.addManagedWorktreeFixture(t, "lane")
	restore := hideGitInRepo(t, r.mainRoot)
	defer restore()

	_, err := r.removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "inspecting the target lock") {
		t.Fatalf("remove with git hidden: err = %v, want the target-lock-inspection error", err)
	}
}

// TestWorktreeRemove_CrashResidueUnlockFailsOnPermissionDenied covers step
// 3's EvRemoveTarget ActUnlockProceed branch's own `git worktree unlock`
// failure: the target carries this session's own stale marker (crash
// residue) while the session is NOT currently inside it, so Decide resolves
// to ActUnlockProceed — but the unlock command itself fails because its
// internal .git/worktrees/<id> directory is read-only.
//
// REAL git: the failure under test is git's own, removing its lock marker from a
// directory it cannot write.
func TestWorktreeRemove_CrashResidueUnlockFailsOnPermissionDenied(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "lane")
	// Simulate a crash that left this session's own lock behind on a
	// worktree it is not currently in (mirrors
	// TestWorktreeRemove_OwnMarkerCrashResidueAutoUnlocksAndProceeds' setup).
	ownReason := worktree.FormatSessionMarker(r.s.id)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", ownReason, path)

	internalDir := worktreeInternalDir(t, r.mainRoot, path)
	chmodReadOnly(t, internalDir)

	_, err := r.removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "unlocking crash-residue lock") {
		t.Fatalf("remove with the crash-residue unlock failing: err = %v, want the unlocking-crash-residue-lock error", err)
	}
}

// TestWorktreeRemove_SidecarGarbageJSONErrors covers step 5's sidecar-read
// error branch for a genuinely corrupt (not merely absent) sidecar: garbage
// JSON makes worktree.ReadSidecar fail with a decode error, which is NOT
// os.IsNotExist, so remove must surface it as a distinct "reading metadata"
// error rather than the "no metadata sidecar" unmanaged-provenance message.
func TestWorktreeRemove_SidecarGarbageJSONErrors(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")
	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(t, canonicalMain)
	sidecarPath := filepath.Join(metaDir, worktree.EncodeSidecarName("lane")+".json")
	if err := os.WriteFile(sidecarPath, []byte("not valid json{{{"), 0o644); err != nil {
		t.Fatalf("corrupt sidecar: %v", err)
	}

	_, err := r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil || !strings.Contains(err.Error(), "reading metadata") {
		t.Fatalf("remove with a corrupt sidecar: err = %v, want the reading-metadata error", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
		t.Errorf("worktree removed despite the sidecar read failure: %v", statErr)
	}
}

// TestWorktreeRemove_DirtyCheckErrorsOnSecondPorcelainCallSurfacesCleanTreeError
// covers step 6's CleanTree error branch: the earlier lockStateOf call (1st
// `worktree list --porcelain`, step 3) must succeed, but the `git -C <target>
// status --porcelain=v1 --untracked-files=all` call CleanTree makes must
// fail.
func TestWorktreeRemove_DirtyCheckErrorsWhenStatusFails(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	path := sr.addManagedLane(t, "lane")

	sr.failGitArgs("-C", path, "status", "--porcelain=v1", "--untracked-files=all")

	_, err := sr.wt().removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "checking for uncommitted changes") {
		t.Fatalf("remove with the dirtiness-check status call failing: err = %v, want the checking-for-uncommitted-changes error", err)
	}
}

// REAL git: the clean arm proves git really deregistered the worktree, and both
// dirty arms turn on real dirty detection — the scripted model reports every tree
// clean, so a converted dirty refusal would invert while still passing.
func TestWorktreeRemove_BasicDispositionMatrix(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		t.Parallel()
		r := newWorktreeRepo(t)
		canonicalMain := r.canonicalMain(t)
		metaDir := r.metaDir(t, canonicalMain)
		res, err := r.create(t, map[string]any{"name": "clean-lane"})
		if err != nil {
			t.Fatalf("create clean-lane: %v", err)
		}
		cleanPath := res["path"].(string)

		out, err := r.removeOp(t, map[string]any{"name": "clean-lane"})
		if err != nil {
			t.Fatalf("clean remove: %v", err)
		}
		if out["path"] != cleanPath {
			t.Errorf("clean remove path = %v, want %s", out["path"], cleanPath)
		}
		if out["branch_deleted"] != false {
			t.Errorf("clean branch_deleted = %v, want false (delete_branch not requested)", out["branch_deleted"])
		}
		if _, statErr := os.Stat(cleanPath); !os.IsNotExist(statErr) {
			t.Errorf("clean-lane worktree dir survived remove: err=%v", statErr)
		}
		out2 := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
		for _, e := range worktree.ParsePorcelain(out2) {
			if filepath.Clean(e.Path) == filepath.Clean(cleanPath) {
				t.Errorf("git worktree list still shows removed clean-lane: %+v", e)
			}
		}
		// delete_branch was not requested: the branch survives, and the sidecar
		// stays (marked worktree_removed) per spec §5 remove step 10.
		if !branchExistsInRepo(t, r.mainRoot, "clean-lane") {
			t.Error("clean-lane branch removed despite delete_branch not being requested")
		}
		sc, scErr := worktree.ReadSidecar(metaDir, "clean-lane")
		if scErr != nil {
			t.Fatalf("read clean-lane sidecar: %v", scErr)
		}
		if !sc.WorktreeRemoved {
			t.Error("clean-lane sidecar worktree_removed not marked true")
		}
		if sc.TipSHAAtRemoval != r.head {
			t.Errorf("clean-lane sidecar tip_sha_at_removal = %q, want %q", sc.TipSHAAtRemoval, r.head)
		}
	})

	t.Run("dirty refusal", func(t *testing.T) {
		t.Parallel()
		r := newWorktreeRepo(t)
		res, err := r.create(t, map[string]any{"name": "dirty-lane"})
		if err != nil {
			t.Fatalf("create dirty-lane: %v", err)
		}
		dirtyPath := res["path"].(string)
		if err := os.WriteFile(filepath.Join(dirtyPath, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatalf("write dirty-lane dirty file: %v", err)
		}
		before := r.s.currentEnv().WorkingDirectory()
		_, err = r.removeOp(t, map[string]any{"name": "dirty-lane"})
		if err == nil {
			t.Fatal("expected remove of dirty-lane without force to error")
		}
		if !strings.Contains(err.Error(), "has uncommitted changes") {
			t.Errorf("dirty-lane error = %q, want it to explain uncommitted changes", err.Error())
		}
		if !strings.Contains(err.Error(), "dirty.txt") {
			t.Errorf("dirty-lane error must list the offending file, got: %v", err)
		}
		if got := r.s.currentEnv().WorkingDirectory(); got != before {
			t.Errorf("env changed on a refused dirty-lane remove: got %q, want unchanged %q", got, before)
		}
		if _, statErr := os.Stat(dirtyPath); statErr != nil {
			t.Errorf("dirty-lane removed despite the refusal: %v", statErr)
		}
	})

	t.Run("force dirty", func(t *testing.T) {
		t.Parallel()
		r := newWorktreeRepo(t)
		res, err := r.create(t, map[string]any{"name": "force-dirty-lane"})
		if err != nil {
			t.Fatalf("create force-dirty-lane: %v", err)
		}
		forcePath := res["path"].(string)
		if err := os.WriteFile(filepath.Join(forcePath, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatalf("write force-dirty-lane dirty file: %v", err)
		}
		if _, err := r.removeOp(t, map[string]any{"name": "force-dirty-lane", "force_dirty": true}); err != nil {
			t.Fatalf("force_dirty remove: %v", err)
		}
		if _, statErr := os.Stat(forcePath); !os.IsNotExist(statErr) {
			t.Errorf("force-dirty-lane worktree dir survived remove: err=%v", statErr)
		}
	})
}

// --- 4: delete_branch on a merged branch deletes with -D after the gate ---

// REAL git: the gate's MERGED verdict comes from real ancestry after a real
// fast-forward, and the branch really leaves the ref store.
func TestWorktreeRemove_DeleteBranchMergedDeletesAfterGate(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "lane")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")

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
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, canonicalMain), "lane"); !os.IsNotExist(scErr) {
		t.Errorf("sidecar survived: err=%v", scErr)
	}
}

// TestWorktreeRemove_DeleteBranchTipLookupErrorsKeepsBranch covers
// worktreeRemove's own delete_branch tipErr branch: the `rev-parse --verify
// refs/heads/<name>` call the branch-tip lookup makes fails, surfaced as a
// "branch not found" BranchKeptReason rather than aborting the whole call
// (the worktree is already removed by step 8 at this point).
func TestWorktreeRemove_DeleteBranchTipLookupErrorsKeepsBranch(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	sr.addManagedLane(t, "lane")

	sr.failGitArgs("rev-parse", "--verify", "refs/heads/lane")

	out, err := sr.wt().removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false", out["branch_deleted"])
	}
	reason, _ := out["branch_kept_reason"].(string)
	if !strings.Contains(reason, "not found") {
		t.Errorf("branch_kept_reason = %q, want it to explain the branch lookup failed", reason)
	}
	// The branch itself is untouched (the failure was in inspecting it, not
	// deleting it) — it still exists.
	if !sr.branchExists(t, "lane") {
		t.Error("branch removed despite the tip lookup failing")
	}
}

// TestWorktreeRemove_DeleteBranchMergeTargetUnknownRefusesWithEvidence
// covers worktree.Merged's TargetUnknown arm surfacing through
// worktreeRemove's own delete_branch evidence message: the lane's recorded
// merge_target branch no longer exists anywhere (deleted after the lane was
// created), so Merged cannot judge it at all.
//
// REAL git: TargetUnknown is a fact about the real ref store — neither a local
// nor a remote-tracking ref by that name resolves — and the lane must carry a
// real commit past its base for the gate to be consulted at all.
func TestWorktreeRemove_DeleteBranchMergeTargetUnknownRefusesWithEvidence(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	wtGit(t, r.mainRoot, "checkout", "-q", "-b", "feature")
	path := r.addManagedWorktreeFixture(t, "lane")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")
	// The recorded merge_target ("feature") is gone: the lane's sidecar still
	// names it, but no local or remote-tracking ref by that name exists.
	wtGit(t, r.mainRoot, "checkout", "-q", "main")
	wtGit(t, r.mainRoot, "branch", "-D", "feature")

	canonicalMain := r.canonicalMain(t)
	sc, scErr := worktree.ReadSidecar(r.metaDir(t, canonicalMain), "lane")
	if scErr != nil {
		t.Fatalf("read sidecar: %v", scErr)
	}
	if sc.MergeTarget != "feature" {
		t.Fatalf("fixture invalid: sidecar merge_target = %q, want feature", sc.MergeTarget)
	}

	out, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (merge target unknown)", out["branch_deleted"])
	}
	reason, _ := out["branch_kept_reason"].(string)
	if !strings.Contains(reason, "merge target unknown") {
		t.Errorf("branch_kept_reason = %q, want it to explain the merge target is unknown", reason)
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch removed despite an unknown merge target")
	}
}

// TestWorktreeRemove_DeleteSidecarFailsOnPermissionDenied covers step 10's
// sidecar-deletion error branch (the branch-deleted arm): the metadata
// directory is made read-only right before the merged-branch delete lands,
// so the branch itself deletes cleanly but removing its now-orphaned
// sidecar file fails with a genuine permission error.
//
// REAL git: reaching the branch-deleted arm at all needs a real MERGED verdict
// over real commits, and the assertion that only the sidecar cleanup failed is
// checked against the real ref store.
func TestWorktreeRemove_DeleteSidecarFailsOnPermissionDenied(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "lane")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")
	wtGit(t, r.mainRoot, "merge", "--ff-only", "lane")

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(t, canonicalMain)
	chmodReadOnly(t, metaDir)

	_, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err == nil || !strings.Contains(err.Error(), "deleting sidecar") {
		t.Fatalf("remove with a read-only metaDir: err = %v, want the deleting-sidecar error", err)
	}
	// The branch itself really was deleted (git's own dir, unaffected by
	// metaDir's permissions) — only the sidecar cleanup failed.
	if branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch survived despite the merged-branch delete gate passing")
	}
}

// TestWorktreeRemove_MarkSidecarRemovedFailsOnPermissionDenied covers step
// 10's sidecar-mark-removed error branch (the survives, not-deleted arm):
// the sidecar FILE itself (not its directory) is made read-only, so
// UpdateSidecar's read succeeds but its truncating write fails.
func TestWorktreeRemove_MarkSidecarRemovedFailsOnPermissionDenied(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(t, canonicalMain)
	sidecarPath := filepath.Join(metaDir, worktree.EncodeSidecarName("lane")+".json")
	if os.Getuid() == 0 {
		t.Skip("running as root: file permissions do not restrict writes")
	}
	if err := os.Chmod(sidecarPath, 0o444); err != nil {
		t.Fatalf("chmod sidecar read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sidecarPath, 0o644) })

	// No delete_branch: the worktree itself is removed cleanly (step 8), but
	// marking the surviving sidecar worktree_removed (step 10) fails.
	_, err := r.removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "marking sidecar removed") {
		t.Fatalf("remove with a read-only sidecar file: err = %v, want the marking-sidecar-removed error", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, ".git")); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived despite step 8 succeeding: err=%v", statErr)
	}
}

// --- 5: delete_branch on an unmerged branch refuses with evidence, keeps the branch and sidecar ---

// REAL git: the refusal needs a genuinely UNMERGED tip. The scripted model's
// `merge-base --is-ancestor` always succeeds, so every lane reads as merged and
// this assertion would invert while still passing.
func TestWorktreeRemove_DeleteBranchUnmergedRefusesEvidenceSidecarKept(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "lane")
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, path, "add", "a.txt")
	wtGit(t, path, "commit", "-m", "advance lane")
	laneTip := strings.TrimSpace(wtGit(t, path, "rev-parse", "HEAD"))

	// main is NOT advanced: lane is never merged.
	logPath := gitArgvRecordingRepoShim(t, r.mainRoot)

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
	if !strings.Contains(reason, `branch "lane" is not merged into`) {
		t.Errorf("branch_kept_reason = %q, want it to say the branch is not merged into the target", reason)
	}
	if b, readErr := os.ReadFile(logPath); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "branch" && fields[1] == "-d" {
				t.Fatalf("git branch -d was invoked despite serf's merge gate: %q", line)
			}
		}
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch was deleted despite being unmerged")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir survived remove: err=%v", statErr)
	}

	canonicalMain := r.canonicalMain(t)
	sc, scErr := worktree.ReadSidecar(r.metaDir(t, canonicalMain), "lane")
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

// REAL git: the trap only exists against real git — `branch -d` under a real
// detached HEAD at the lane's tip would succeed, and the argv log that proves
// serf never issued it comes from a real git shim.
func TestWorktreeRemove_DetachedHeadReviewRefusesNeverInvokesLowercaseD(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "feature")
	if err := os.WriteFile(filepath.Join(path, "f.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	wtGit(t, path, "add", "f.txt")
	wtGit(t, path, "commit", "-m", "advance feature")
	featureTip := strings.TrimSpace(wtGit(t, path, "rev-parse", "HEAD"))

	// Detach the main checkout's HEAD directly at feature's tip. Under a
	// review-of-the-tip workflow this is exactly the scenario rev-6 review
	// caught: `git branch -d feature` run from here would see feature as
	// trivially merged into the current (detached) HEAD and succeed — but
	// main's actual branch ref never moved, so serf's own gate (which never
	// consults HEAD) must refuse.
	wtGit(t, r.mainRoot, "checkout", "--detach", featureTip)

	logPath := gitArgvRecordingRepoShim(t, r.mainRoot)

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

// REAL git: the only refusal in play is git's own one-checkout-per-branch rule,
// and the surfaced location is the path git itself reports.
func TestWorktreeRemove_BranchCheckedOutElsewhereSurfacesLocation(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	r.addManagedWorktreeFixture(t, "lane")

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
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	path := sr.addManagedLane(t, "lane")

	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000003")
	sr.setLaneLock(t, path, foreignReason)

	_, err := sr.wt().removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove of a foreign-locked target to be refused even with force")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error must name the foreign lock reason %q, got: %v", foreignReason, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the foreign-lock refusal: %v", statErr)
	}
	if got := sr.laneLockReason(t, path); got != foreignReason {
		t.Errorf("foreign lock mutated: got %q, want unchanged %q", got, foreignReason)
	}
}

// --- 9: own-marker crash residue auto-unlocks and proceeds ---

// REAL git: the proof that the auto-unlock happened is that git's own `worktree
// remove` — which refuses a LOCKED entry — then succeeded. The scripted model
// does not check locks on remove, so a converted version would pass even if the
// unlock were skipped entirely.
func TestWorktreeRemove_OwnMarkerCrashResidueAutoUnlocksAndProceeds(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "lane")

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

// TestWorktreeRemove_RemoveCurrentUnlockBeforeRestoreFailsOnPermissionDenied
// covers step 7's unlockAtRestore branch's own `git worktree unlock`
// failure: the session is currently inside the target with its own marker
// (EvRemoveCurrent -> ActUnlock), but the internal .git/worktrees/<id>
// directory backing the target has been made read-only.
//
// REAL git: the failure under test is git's own, removing its lock marker from a
// directory it cannot write.
func TestWorktreeRemove_RemoveCurrentUnlockBeforeRestoreFailsOnPermissionDenied(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if !r.porcelainEntry(t, path).Locked {
		t.Fatal("fixture invalid: expected the lane to be locked (session is inside it)")
	}

	internalDir := worktreeInternalDir(t, r.mainRoot, path)
	chmodReadOnly(t, internalDir)

	_, err = r.removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "unlocking before restore") {
		t.Fatalf("remove-current with the unlock-before-restore failing: err = %v, want the unlocking-before-restore error", err)
	}
}

// TestWorktreeRemove_GitWorktreeRemoveCommandFails covers step 8's own
// `git worktree remove` failure branch: every earlier check (lock, live
// work, sidecar ownership, dirtiness) passes, but the removal command itself
// fails.
func TestWorktreeRemove_GitWorktreeRemoveCommandFails(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	path := sr.addManagedLane(t, "lane")

	sr.failGitArgs("worktree", "remove", "--", path)

	_, err := sr.wt().removeOp(t, map[string]any{"name": "lane"})
	if err == nil || !strings.Contains(err.Error(), "git worktree remove failed") {
		t.Fatalf("remove with the git worktree remove command failing: err = %v, want the git-worktree-remove-failed error", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
		t.Errorf("worktree gone despite the remove command failing: %v", statErr)
	}
}

// REAL git: the restore-land rule's effect is a lock that really lands in git's
// registry on a lane git itself reports as unlocked, and the removed lane really
// leaves the registry.
func TestWorktreeRemove_RemoveCurrentRestoresAndRelocks(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	canonicalMain := r.canonicalMain(t)
	launchPath := r.managedPath(t, canonicalMain, "launch")
	sr.addLane(t, "launch", launchPath)

	// A session launched directly inside a managed worktree, having never
	// gone through create/switch — no restore env was ever saved. Give it a
	// real sidecar (as a genuine prior `create` would have) so the isolated
	// behavior under test is step 7's no-safe-restore-env refusal, not step
	// 5's unmanaged-provenance refusal.
	r2 := sr.sessionAt(t, launchPath).wt()
	s2 := r2.s
	metaDir := r2.metaDir(t, canonicalMain)
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

// TestWorktreeRemove_RemoveCurrentNoSafeRestoreEnvRefusesThroughSymlinkedLaunch
// is the symlinked-launch-path variant of
// TestWorktreeRemove_RemoveCurrentNoSafeRestoreEnvRefuses: the session's
// active root reaches the same managed worktree through a differently-spelled
// symlink rather than the canonical join of projectDir+name. The
// currentlyInside comparison in worktreeRemove must canonicalize the active
// root the same way canonicalTarget is already canonicalized, or this reads
// as "not inside" and mis-routes into the non-inside removal path — deleting
// the directory the session is actually rooted in, out from under it, with no
// safe-restore-env refusal and no warning.
func TestWorktreeRemove_RemoveCurrentNoSafeRestoreEnvRefusesThroughSymlinkedLaunch(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	canonicalMain := r.canonicalMain(t)
	launchPath := r.managedPath(t, canonicalMain, "launch")
	sr.addLane(t, "launch", launchPath)

	// A symlink elsewhere pointing at the real worktree directory, spelled
	// differently from the canonical projectDir+name join.
	aliasDir := t.TempDir()
	aliasPath := filepath.Join(aliasDir, "launch-alias")
	if err := os.Symlink(launchPath, aliasPath); err != nil {
		t.Fatalf("symlink launch alias: %v", err)
	}

	// A session launched directly inside the worktree via the symlinked
	// spelling, having never gone through create/switch — no restore env was
	// ever saved. Give it a real sidecar (as a genuine prior `create` would
	// have) so the isolated behavior under test is step 7's
	// no-safe-restore-env refusal, not step 5's unmanaged-provenance refusal.
	r2 := sr.sessionAt(t, aliasPath).wt()
	s2 := r2.s
	metaDir := r2.metaDir(t, canonicalMain)
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
		t.Fatal("expected remove-current (via symlinked launch path) with no safe restore env to be refused")
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Errorf("error should explain the missing restore env, got: %v", err)
	}
	if _, statErr := os.Stat(launchPath); statErr != nil {
		t.Errorf("launch worktree removed despite the refusal: %v", statErr)
	}
	if got := s2.currentEnv().WorkingDirectory(); got != aliasPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want unchanged %q", got, aliasPath)
	}
}

// --- 12: live-work guard, via the test-only stub seam ---

func TestWorktreeRemove_LiveWorkGuardRefusesViaStub(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")

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

	_, err := r.removeOp(t, map[string]any{"name": "lane", "force": true})
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

// TestWorktreeRemove_CrossCreatorUnlockedLaneProceeds covers the F1
// relaxation: an UNLOCKED lane created by another session is routine cleanup —
// the occupancy lock (not creator identity) is the safety mechanism, so remove
// proceeds without force. (A foreign-LOCKED lane is still refused at step 3;
// committed work is still protected by the merge gate; uncommitted work by
// force_dirty.) This inverts the pre-F1 cross-creator refusal.
func TestWorktreeRemove_CrossCreatorUnlockedLaneProceeds(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	path := sr.addManagedLane(t, "shared")

	r2 := sr.sessionAt(t, sr.mainRoot).wt()
	if r2.s.id == sr.s.id {
		t.Fatal("sessionAt returned the same session id")
	}

	if _, err := r2.removeOp(t, map[string]any{"name": "shared"}); err != nil {
		t.Fatalf("cross-creator remove of an unlocked lane should proceed without force, got: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived the cross-creator remove: err=%v", statErr)
	}
}

// TestWorktreeRemove_ForceDoesNotDiscardUncommitted covers the F3 fix: force
// overrides provenance/merge gating but NOT uncommitted work; a dirty tree is
// still refused (needs force_dirty), so forcing past a provenance refusal
// cannot silently discard an edit. The live S5 eval caught exactly this loss.
//
// REAL git: the whole test turns on real dirty detection. The scripted model
// reports every tree clean, so the force refusal would silently stop firing while
// the test still "passed".
func TestWorktreeRemove_ForceDoesNotDiscardUncommitted(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	path := r.addManagedWorktreeFixture(t, "dirtylane")
	// dirty the lane's checkout (uncommitted edit)
	if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main // dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty write: %v", err)
	}

	// force:true (no force_dirty) must still refuse and preserve the edit.
	_, err := r.removeOp(t, map[string]any{"name": "dirtylane", "force": true})
	if err == nil {
		t.Fatal("force alone must not discard an uncommitted edit")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") || !strings.Contains(err.Error(), "force_dirty") {
		t.Errorf("dirty refusal should name uncommitted changes and force_dirty, got: %v", err)
	}

	// force_dirty:true removes it.
	if _, err := r.removeOp(t, map[string]any{"name": "dirtylane", "force_dirty": true}); err != nil {
		t.Fatalf("force_dirty remove should succeed: %v", err)
	}
}

// --- 14: remove-current whose restore lands in a foreign-locked managed
// launch root surfaces a warning (mirrors
// TestWorktreeExit_RestoringIntoManagedLaunchRootIdempotentRelockForeignWarns,
// exercising applyRestoreLandRelock's ActWarnCoOccupy arm through remove's
// own dispatch-layer warning plumbing rather than exit's) ---

func TestWorktreeRemove_RemoveCurrentForeignLockedRestoreWarns(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	launch, launchPath, pathWork := sr.launchSession(t)
	s2, r2 := launch.s, launch.wt()

	// Simulate another session/tool claiming the launch worktree while s2 was
	// away in "work".
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000002")
	sr.setLaneLock(t, launchPath, foreignReason)

	out, err := r2.removeOp(t, map[string]any{"name": "work"})
	if err != nil {
		t.Fatalf("remove-current must warn-and-continue on a foreign-locked restore target, not refuse: %v", err)
	}
	if out["path"] != pathWork {
		t.Errorf("remove path = %v, want %s", out["path"], pathWork)
	}
	warning, _ := out["warning"].(string)
	if warning == "" {
		t.Fatal("expected a surfaced warning for a foreign-locked restore target")
	}
	if !strings.Contains(warning, foreignReason) {
		t.Errorf("warning = %q, want it to name the foreign reason %q", warning, foreignReason)
	}
	if msg, _ := out["message"].(string); !strings.Contains(msg, "Warning:") {
		t.Errorf("message = %q, want it to append the Warning: suffix", msg)
	}

	// The session still lands there despite the foreign lock (a restore
	// cannot be refused), and the removed worktree is gone.
	if got := s2.currentEnv().WorkingDirectory(); got != launchPath {
		t.Errorf("currentEnv WorkingDirectory = %q, want %q", got, launchPath)
	}
	if _, statErr := os.Stat(pathWork); !os.IsNotExist(statErr) {
		t.Errorf("work worktree survived remove-current: err=%v", statErr)
	}
	// The foreign lock on the launch worktree is left untouched (co-occupy,
	// not a forced takeover).
	if got := sr.laneLockReason(t, launchPath); got != foreignReason {
		t.Errorf("launch worktree lock = %q, want untouched %q", got, foreignReason)
	}
}
