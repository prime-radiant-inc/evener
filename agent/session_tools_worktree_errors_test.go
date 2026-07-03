package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/llm"
)

// This file is the spec §8 error-surface catalog: one test per §8 bullet,
// each asserting the error text contains the bullet's named elements
// verbatim, plus the §2 same-response ordering test. Row numbering below
// matches §8's bullet order top to bottom (docs/superpowers/specs/
// 2026-07-02-native-worktree-tools-design.md). Behavioral side effects
// (locks, sidecars, env mutation) for these same scenarios are already
// covered in depth by session_tools_worktree_{create,switch,remove,prune}_test.go
// (Tasks 13-16); this file exists to make the error-TEXT contract explicit
// and exhaustive against the spec table, reusing those files' wtRepo/wtGit
// fixtures (same package).
//
// Row 8 ("delegate_send to a delegate whose isolation worktree was
// disposed") is STILL OUT OF SCOPE here: Task 21 built spawn/restore/revival
// (creation, the manage_worktree deny, and the §7 re-lock rule — see
// job_delegate_isolation_test.go) and the `DelegateRestoreDescriptor.Isolation`
// field now exists, but close-time disposal (spec §9 lifecycle steps 4-5,
// which is what actually produces a "disposed" isolation worktree) is not
// built yet — that is Task 22. There is nothing to test for this row until
// disposal exists.

// --- Row 1: not in a git repo -> create errors with a clear message ---

func TestWorktreeErrors_NotInGitRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir() // no `git init`
	s := newSession(t, withDir(dir))

	_, err := s.worktreeCreate(context.Background(), "x", "")
	if err == nil {
		t.Fatal("expected create outside a git repo to error")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "not in a git repository")
	}
}

// --- Row 2: name fails validation -> error before any git call ---

func TestWorktreeErrors_NameFailsValidationBeforeAnyGitCall(t *testing.T) {
	r := newWorktreeRepo(t)
	logPath := gitArgvRecordingShim(t)

	_, err := r.create(t, map[string]any{"name": "has space"})
	if err == nil {
		t.Fatal("expected create to reject an invalid name")
	}
	wantElem := worktree.ValidateName("has space").Error() // exact ValidateName message
	if !strings.Contains(err.Error(), wantElem) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantElem)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		b, _ := os.ReadFile(logPath)
		t.Errorf("git was invoked before the name-validation error fired: %s", string(b))
	}
}

// --- Row 3: base_ref fails validation or cannot resolve -> error before `git worktree add` ---

func TestWorktreeErrors_BadBaseRefBeforeWorktreeAdd(t *testing.T) {
	cases := []struct {
		name, ref, wantElem string
	}{
		{"leading-dash", "-x", `base_ref "-x" must not start with "-"`},
		{"unresolvable", "no-such-ref", `base_ref "no-such-ref" cannot be resolved to a commit from`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newWorktreeRepo(t)
			logPath := gitArgvRecordingShim(t)

			_, err := r.create(t, map[string]any{"name": c.name, "base_ref": c.ref})
			if err == nil {
				t.Fatalf("expected create to reject base_ref %q", c.ref)
			}
			if !strings.Contains(err.Error(), c.wantElem) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantElem)
			}
			if b, readErr := os.ReadFile(logPath); readErr == nil && strings.Contains(string(b), "worktree add") {
				t.Errorf("git worktree add was invoked despite the rejected base_ref: %s", string(b))
			}
		})
	}
}

// --- Row 4: name already exists as a branch or worktree -> create errors; suggest switch only when managed ---

func TestWorktreeErrors_NameExistsSuggestsSwitchOnlyWhenManaged(t *testing.T) {
	t.Run("unmanaged branch: no switch suggestion", func(t *testing.T) {
		r := newWorktreeRepo(t)
		wtGit(t, r.mainRoot, "branch", "plain", r.head)

		_, err := r.create(t, map[string]any{"name": "plain"})
		if err == nil {
			t.Fatal("expected create to reject an existing branch name")
		}
		if !strings.Contains(err.Error(), `branch "plain" already exists`) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), `branch "plain" already exists`)
		}
		if strings.Contains(err.Error(), "switch") {
			t.Errorf("error = %q, must not suggest switch when no managed worktree exists", err.Error())
		}
	})

	t.Run("managed worktree: switch suggested", func(t *testing.T) {
		r := newWorktreeRepo(t)
		if _, err := r.create(t, map[string]any{"name": "dup"}); err != nil {
			t.Fatalf("first create: %v", err)
		}
		_, err := r.create(t, map[string]any{"name": "dup"})
		if err == nil {
			t.Fatal("expected create to reject a re-created managed name")
		}
		if !strings.Contains(err.Error(), `branch "dup" already exists`) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), `branch "dup" already exists`)
		}
		if !strings.Contains(err.Error(), "use manage_worktree switch to enter its worktree") {
			t.Errorf("error = %q, want a switch suggestion for an existing managed worktree", err.Error())
		}
	})
}

// --- Row 5: switch/remove to a nonexistent worktree -> error ---

func TestWorktreeErrors_SwitchToNonexistentWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	_, err := r.switchOp(t, map[string]any{"name": "never-created"})
	if err == nil {
		t.Fatal("expected switch to a nonexistent worktree to error")
	}
	if !strings.Contains(err.Error(), "no worktree at") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no worktree at")
	}
}

func TestWorktreeErrors_RemoveNonexistentWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	_, err := r.removeOp(t, map[string]any{"name": "never-created"})
	if err == nil {
		t.Fatal("expected remove of a nonexistent worktree to error")
	}
	// No worktree ever existed at this name, so there is no sidecar either;
	// remove's cross-session ownership guard (spec §5 remove step 5) refuses
	// unmanaged-provenance targets without force before ever reaching git.
	if !strings.Contains(err.Error(), "has no metadata sidecar") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "has no metadata sidecar")
	}
}

// --- Row 6: switch by path to a path not in `git worktree list` -> error ---

func TestWorktreeErrors_SwitchByPathUnregistered(t *testing.T) {
	r := newWorktreeRepo(t)
	stray := t.TempDir()

	_, err := r.switchOp(t, map[string]any{"path": stray})
	if err == nil {
		t.Fatal("expected switch by an unregistered path to error")
	}
	if !strings.Contains(err.Error(), "is not a worktree registered to this repository") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "is not a worktree registered to this repository")
	}
}

// --- Row 7: switch/remove on a worktree locked by another session/delegate ->
// error naming the lock reason; force does not override locks; an own-marker
// lock is adopted/released, never a raw git fatal ---

func TestWorktreeErrors_SwitchForeignSessionLockNamesReason(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000009")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, path)

	_, err = r.switchOp(t, map[string]any{"name": "lane"})
	if err == nil {
		t.Fatal("expected switch to a foreign-session-locked worktree to error")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error = %q, want it to contain the foreign lock reason %q", err.Error(), foreignReason)
	}
}

func TestWorktreeErrors_SwitchForeignDelegateLockNamesReason(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	delegateReason := worktree.FormatDelegateMarker("dlg_01FOREIGNDELEGATE0001", "01FOREIGNPARENTSESSION02")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", delegateReason, path)

	_, err = r.switchOp(t, map[string]any{"name": "lane"})
	if err == nil {
		t.Fatal("expected switch to a delegate-locked worktree to error")
	}
	if !strings.Contains(err.Error(), delegateReason) {
		t.Errorf("error = %q, want it to contain the delegate lock reason %q", err.Error(), delegateReason)
	}
}

func TestWorktreeErrors_RemoveForeignLockRefusesForceDoesNotOverride(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	foreignReason := worktree.FormatSessionMarker("01FOREIGNSESSIONID0000010")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreignReason, path)

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove of a foreign-locked worktree to error even with force")
	}
	if !strings.Contains(err.Error(), foreignReason) {
		t.Errorf("error = %q, want it to contain the foreign lock reason %q", err.Error(), foreignReason)
	}
	if !strings.Contains(err.Error(), "force does not override a lock") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "force does not override a lock")
	}
}

func TestWorktreeErrors_RemoveOwnMarkerCrashResidueNeverARawGitFatal(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// Own-marker crash residue: locked with this session's own marker on a
	// worktree it is not currently in.
	ownReason := worktree.FormatSessionMarker(r.s.id)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", ownReason, path)

	// Must auto-release and proceed -- not surface a raw `git worktree
	// remove` "already locked" fatal.
	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove of own-marker crash residue must proceed cleanly, got: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived a remove of its own crash-residue lock: err=%v", statErr)
	}
}

// --- Row 9: remove target resolves outside the managed worktree directory -> error ---

func TestWorktreeErrors_RemoveTargetResolvesOutsideManagedDir(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	target := r.managedPath(canonicalMain, "escape")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir managed dir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, target); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}

	_, err := r.removeOp(t, map[string]any{"name": "escape", "force": true})
	if err == nil {
		t.Fatal("expected remove of a target resolving outside the managed dir to error")
	}
	if !strings.Contains(err.Error(), "is not a managed worktree") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "is not a managed worktree")
	}
}

// --- Row 10: exit when not in a worktree -> clear, non-destructive error ---

func TestWorktreeErrors_ExitNotInWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	before := r.s.currentEnv().WorkingDirectory()

	_, err := r.exitOp(t)
	if err == nil {
		t.Fatal("expected exit outside a worktree to error")
	}
	if !strings.Contains(err.Error(), "not in a worktree") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "not in a worktree")
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != before {
		t.Errorf("exit mutated the env on a refused (non-destructive) call: got %q, want unchanged %q", got, before)
	}
	if r.s.worktreeRestoreEnv != nil {
		t.Error("exit mutated the saved restore env on a refused call")
	}
}

// --- Row 11: remove on a dirty worktree without force -> error listing the
// dirty files, without changing the session env ---

func TestWorktreeErrors_RemoveDirtyWithoutForceListsFilesEnvUnchanged(t *testing.T) {
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
	if !strings.Contains(err.Error(), "has uncommitted changes") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "has uncommitted changes")
	}
	if !strings.Contains(err.Error(), "dirty.txt") {
		t.Errorf("error = %q, want it to list the offending file %q", err.Error(), "dirty.txt")
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != before {
		t.Errorf("env changed on a refused remove: got %q, want unchanged %q", got, before)
	}
}

// --- Row 12: remove with delete_branch on an unmerged branch without force ->
// worktree removed, branch deletion refused by serf's merge-target gate with
// unmerged evidence (never git branch -d), sidecar retained as
// branch-residue record ---

func TestWorktreeErrors_RemoveDeleteBranchUnmergedRefusesEvidenceSidecarKept(t *testing.T) {
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
	// main is never advanced: lane is never merged.

	logPath := gitArgvRecordingShim(t)

	out, err := r.removeOp(t, map[string]any{"name": "lane", "delete_branch": true})
	if err != nil {
		t.Fatalf("remove (worktree removal itself must succeed): %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree survived remove: err=%v", statErr)
	}
	if out["branch_deleted"] != false {
		t.Errorf("branch_deleted = %v, want false (branch is unmerged)", out["branch_deleted"])
	}
	reason, _ := out["branch_kept_reason"].(string)
	wantElem := `branch "lane" is not merged into`
	if !strings.Contains(reason, wantElem) {
		t.Errorf("branch_kept_reason = %q, want it to contain %q", reason, wantElem)
	}
	if !branchExistsInRepo(t, r.mainRoot, "lane") {
		t.Error("branch was deleted despite being unmerged")
	}

	// Never git branch -d (HEAD-relative): only -D may ever appear, and only
	// on a passing gate -- here the gate refused, so neither should run.
	if b, readErr := os.ReadFile(logPath); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "branch" && fields[1] == "-d" {
				t.Fatalf("git branch -d was invoked (must never be, gated by serf's own merge check): %q", line)
			}
		}
	}

	// Sidecar retained as a branch-residue record.
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

// --- Row 13: git older than the `worktree add --lock --reason` floor ->
// preflight error naming the required version; no degraded mode ---

func TestWorktreeErrors_GitTooOldPreflightNamesRequiredVersionNoDegradedMode(t *testing.T) {
	r := newWorktreeRepo(t)

	shimDir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then echo \"git version 2.20.0\"; exit 0; fi\n" +
		"echo \"shim: unexpected git $*\" >&2; exit 1\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := r.create(t, map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("expected create to refuse an ancient git")
	}
	for _, wantElem := range []string{"too old", "2.33", "no degraded mode"} {
		if !strings.Contains(err.Error(), wantElem) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), wantElem)
		}
	}
}

// --- Row 14: remove when live children/delegates/shell jobs are rooted
// under the target -> error (live work guard) ---

func TestWorktreeErrors_RemoveLiveWorkGuard(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	r.s.worktreeLiveWorkStub = func(p string) []string {
		if p == filepath.Clean(path) {
			return []string{"job_errsurface1 (shell, running)"}
		}
		return nil
	}
	t.Cleanup(func() { r.s.worktreeLiveWorkStub = nil })

	_, err = r.removeOp(t, map[string]any{"name": "lane", "force": true})
	if err == nil {
		t.Fatal("expected remove to be refused by the live-work guard")
	}
	if !strings.Contains(err.Error(), "live work under") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "live work under")
	}
	if !strings.Contains(err.Error(), "job_errsurface1") {
		t.Errorf("error = %q, want it to surface the live-work evidence %q", err.Error(), "job_errsurface1")
	}
}

// --- Row 15: remove of a worktree created by another session without force
// -> error naming the creator ---

func TestWorktreeErrors_RemoveCrossCreatorNamesCreator(t *testing.T) {
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

	_, err = r2.removeOp(t, map[string]any{"name": "shared"})
	if err == nil {
		t.Fatal("expected a cross-creator remove without force to error")
	}
	if !strings.Contains(err.Error(), "was created by a different session") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "was created by a different session")
	}
	if !strings.Contains(err.Error(), r.s.id) {
		t.Errorf("error = %q, want it to name the creator session %q", err.Error(), r.s.id)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite the cross-creator refusal: %v", statErr)
	}
}

// --- Row 16: remove of the active worktree with no safe restore env -> error ---

func TestWorktreeErrors_RemoveCurrentNoSafeRestoreEnv(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	launchPath := r.managedPath(canonicalMain, "launch")
	if err := os.MkdirAll(filepath.Dir(launchPath), 0o755); err != nil {
		t.Fatalf("mkdir launch parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "launch", launchPath, r.head)

	// A session launched directly inside a managed worktree (never entering
	// via create/switch) has no saved restore env.
	s2 := newSession(t, withDir(launchPath))
	s2.stateDir = r.stateDir
	r2 := &wtRepo{s: s2, mainRoot: r.mainRoot, stateDir: r.stateDir, head: r.head}
	metaDir := r2.metaDir(canonicalMain)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metaDir: %v", err)
	}
	sc := worktree.Sidecar{
		Name: "launch", Branch: "launch", BaseSHA: r.head,
		OriginalRoot: canonicalMain, CreatorSession: s2.id,
		CreatedAt: s2.sclock().Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if err := worktree.WriteSidecarExcl(metaDir, "launch", sc); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	_, err := r2.removeOp(t, map[string]any{"name": "launch"})
	if err == nil {
		t.Fatal("expected remove-current with no safe restore env to error")
	}
	if !strings.Contains(err.Error(), "no safe restore env") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no safe restore env")
	}
	if !strings.Contains(err.Error(), "refusing to remove the active root") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "refusing to remove the active root")
	}
}

// --- Row 17: prune never errors on skips; reports per-entry skip reasons ---

func TestWorktreeErrors_PruneNeverErrorsReportsPerEntrySkipReasons(t *testing.T) {
	r := newWorktreeRepo(t)

	// A locked entry (session stays inside it).
	if _, err := r.create(t, map[string]any{"name": "locked-lane"}); err != nil {
		t.Fatalf("create locked-lane: %v", err)
	}

	// A second session for a dirty entry, so the first session's occupancy
	// of locked-lane is untouched by the second's create-away.
	r2 := r.secondSession(t)
	res, err := r2.create(t, map[string]any{"name": "dirty-lane"})
	if err != nil {
		t.Fatalf("create dirty-lane: %v", err)
	}
	if err := os.WriteFile(filepath.Join(res["path"].(string), "d.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	if _, err := r2.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune must never error on ordinary skip conditions, got: %v", err)
	}
	skipped := pruneEntries(t, out, "skipped")

	lockedEntry := findPruneEntry(t, skipped, "locked-lane")
	if lockedEntry == nil {
		t.Fatal("locked-lane not reported skipped")
	}
	if reason, _ := lockedEntry["reason"].(string); !strings.Contains(reason, "locked") {
		t.Errorf("locked-lane skip reason = %q, want it to mention locked", reason)
	}

	dirtyEntry := findPruneEntry(t, skipped, "dirty-lane")
	if dirtyEntry == nil {
		t.Fatal("dirty-lane not reported skipped")
	}
	if reason, _ := dirtyEntry["reason"].(string); !strings.Contains(reason, "dirty") {
		t.Errorf("dirty-lane skip reason = %q, want it to mention dirty", reason)
	}
}

// --- Row 18: non-local execution environment -> manage_worktree errors clearly ---

func TestWorktreeErrors_NonLocalExecutionEnvironment(t *testing.T) {
	r := newWorktreeRepo(t)
	r.s.mu.Lock()
	r.s.env = &timeoutEnv{wd: r.mainRoot}
	r.s.mu.Unlock()

	const wantElem = "local execution environment"
	ctx := context.Background()

	if _, err := r.s.worktreeCreate(ctx, "x", ""); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("create: err = %v, want it to contain %q", err, wantElem)
	}
	if _, err := r.s.worktreeSwitchByName(ctx, "x"); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("switch by name: err = %v, want it to contain %q", err, wantElem)
	}
	if _, err := r.s.worktreeSwitchByPath(ctx, "/tmp/x"); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("switch by path: err = %v, want it to contain %q", err, wantElem)
	}
	if _, err := r.s.worktreeRemove(ctx, "x", false, false); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("remove: err = %v, want it to contain %q", err, wantElem)
	}
	if _, err := r.s.worktreeList(ctx); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("list: err = %v, want it to contain %q", err, wantElem)
	}
	if _, err := r.s.worktreePrune(ctx); err == nil || !strings.Contains(err.Error(), wantElem) {
		t.Errorf("prune: err = %v, want it to contain %q", err, wantElem)
	}
	// exit is intentionally not asserted here: its own "not in a worktree"
	// check (spec §4 exit step 1) fires first regardless of env type, since
	// the saved restore env it inspects is independent of the CURRENT env's
	// type. That still satisfies "errors clearly" (row 10), just with a
	// different message; see this file's header and the task-17 report for
	// the full discussion.
}

// --- Row 19: git unavailable -> ResolveMainRepoRoot resolves structurally
// (no git binary needed for the linked-worktree case); lifecycle operations
// require git and error clearly (not panic) if it is absent ---

func TestWorktreeErrors_GitUnavailableLifecycleOpsErrorClearly(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// Hide git entirely.
	t.Setenv("PATH", t.TempDir())
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		t.Skip("git still resolvable after PATH override; cannot prove the no-git path")
	}

	// ResolveMainRepoRoot's structural walk needs no git binary at all (the
	// execenv-level guarantee is proven directly in
	// agent/execenv/gitpath_mainroot_test.go's
	// TestResolveMainRepoRoot_StructuralWithoutGit; this corroborates it at
	// the manage_worktree call site, over the main checkout root itself).
	got := execenv.ResolveMainRepoRoot(r.s.currentEnv().(*execenv.LocalExecutionEnvironment), r.mainRoot)
	if got != r.canonicalMain(t) {
		t.Fatalf("ResolveMainRepoRoot without git = %q, want %q (main root, resolved structurally)", got, r.canonicalMain(t))
	}

	// A lifecycle operation (list) needs an actual git subprocess and must
	// fail cleanly (a non-nil error, not a panic and not a silent empty
	// success) when it is unavailable.
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	_, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "list"})
	if err == nil {
		t.Fatal("expected list to error cleanly when git is unavailable")
	}
	if !strings.Contains(err.Error(), "manage_worktree list:") {
		t.Errorf("error = %q, want it to carry the manage_worktree list: prefix", err.Error())
	}
}

// --- §2 same-response ordering: a read-only call BEFORE manage_worktree in
// the same model response sees the old env; a read-only call AFTER it sees
// the new env ---

// mkToolCall builds an llm.ToolCallData with args JSON-encoded, matching the
// shape execToolBatch/execTool consume (see session_tools_dispatch_fuzz_test.go).
func mkToolCall(id, name string, args map[string]any) llm.ToolCallData {
	b, _ := json.Marshal(args)
	return llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(b), Type: "function"}
}

func TestWorktreeOrdering_ReadBeforeSeesOldEnvReadAfterSeesNewEnv(t *testing.T) {
	r := newWorktreeRepo(t)

	// Pre-create "lane" with distinguishing content, then exit back to
	// mainRoot so this test's batch starts from a known env.
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	lanePath := res["path"].(string)
	if err := os.WriteFile(filepath.Join(lanePath, "marker.txt"), []byte("lane-content\n"), 0o644); err != nil {
		t.Fatalf("write lane marker: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.mainRoot, "marker.txt"), []byte("main-content\n"), 0o644); err != nil {
		t.Fatalf("write main marker: %v", err)
	}

	// Two read_file calls before the switch, two after -- large enough
	// groups to exercise execToolBatch's genuine parallel read-batch
	// dispatch (session_tool_round.go's flushReadBatch "default" branch) on
	// both sides of the serializing manage_worktree call, per spec §2:
	// "Because the tool is non-read-only, execToolBatch serializes it
	// between any read-only batches."
	calls := []llm.ToolCallData{
		mkToolCall("before-1", "read_file", map[string]any{"file_path": "marker.txt"}),
		mkToolCall("before-2", "read_file", map[string]any{"file_path": "marker.txt"}),
		mkToolCall("switch", "manage_worktree", map[string]any{"operation": "switch", "name": "lane"}),
		mkToolCall("after-1", "read_file", map[string]any{"file_path": "marker.txt"}),
		mkToolCall("after-2", "read_file", map[string]any{"file_path": "marker.txt"}),
	}

	profile := r.s.currentProfile()
	if !profile.SupportsParallelToolCalls() {
		t.Fatal("test profile must support parallel tool calls to exercise the read-batch grouping path")
	}

	results, err := r.s.execToolBatch(t.Context(), calls, profile)
	if err != nil {
		t.Fatalf("execToolBatch: %v", err)
	}
	if len(results) != len(calls) {
		t.Fatalf("execToolBatch returned %d results for %d calls", len(results), len(calls))
	}

	for _, idx := range []int{0, 1} {
		res := results[idx]
		if res.IsError {
			t.Fatalf("before-call %d errored: %s", idx, res.FullOutput)
		}
		if !strings.Contains(res.FullOutput, "main-content") {
			t.Errorf("before-call %d output = %q, want it to contain %q (old env, pre-swap)", idx, res.FullOutput, "main-content")
		}
		if strings.Contains(res.FullOutput, "lane-content") {
			t.Errorf("before-call %d output = %q, must NOT contain %q (that would mean it saw the post-swap env)", idx, res.FullOutput, "lane-content")
		}
	}

	if results[2].IsError {
		t.Fatalf("manage_worktree switch errored: %s", results[2].FullOutput)
	}

	for _, idx := range []int{3, 4} {
		res := results[idx]
		if res.IsError {
			t.Fatalf("after-call %d errored: %s", idx, res.FullOutput)
		}
		if !strings.Contains(res.FullOutput, "lane-content") {
			t.Errorf("after-call %d output = %q, want it to contain %q (new env, post-swap)", idx, res.FullOutput, "lane-content")
		}
		if strings.Contains(res.FullOutput, "main-content") {
			t.Errorf("after-call %d output = %q, must NOT contain %q (that would mean it still saw the pre-swap env)", idx, res.FullOutput, "main-content")
		}
	}

	if got := r.s.currentEnv().WorkingDirectory(); got != lanePath {
		t.Errorf("after the batch, currentEnv WorkingDirectory = %q, want %q (the switch must have actually landed)", got, lanePath)
	}
}
