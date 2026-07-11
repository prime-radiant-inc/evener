//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
)

// FuzzWorktreeFaultLifecycleProgram drives the worktree tool's failure and
// reconciliation paths through a real Session. The only modeled boundary is
// Git: sidecar files, path checks, lock choreography, and Session env swaps
// use their production implementations under t.TempDir.
func FuzzWorktreeFaultLifecycleProgram(f *testing.F) {
	for scenario := byte(0); scenario < worktreeFaultScenarioCount; scenario++ {
		f.Add([]byte{scenario, scenario + 17, 0xff - scenario})
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		worktreeFaultLifecycleProgram(t, program)
	})
}

const worktreeFaultScenarioCount = 10

func worktreeFaultLifecycleProgram(t *testing.T, program []byte) {
	t.Helper()
	scenario := 0
	if len(program) > 0 {
		scenario = int(program[0] % worktreeFaultScenarioCount)
	}

	switch scenario {
	case 0:
		worktreeFaultDispatchAndPathProgram(t, program)
	case 1:
		worktreeFaultDelegateRollbackProgram(t, program)
	case 2:
		worktreeFaultRestoreProgram(t, program)
	case 3:
		worktreeFaultRemoveProgram(t, program)
	case 4:
		worktreeFaultPruneSweepOneProgram(t, program)
	case 5:
		worktreeFaultPruneSweepTwoProgram(t, program)
	case 6:
		worktreeFaultListAndRegistryProgram(t, program)
	case 7:
		worktreeFaultSwitchProgram(t, program)
	case 8:
		worktreeFaultCreateAndSwitchGuardsProgram(t, program)
	case 9:
		worktreeFaultOperationErrorsProgram(t, program)
	default:
		t.Fatalf("unknown worktree fault scenario %d", scenario)
	}
}

func worktreeFaultDispatchAndPathProgram(t *testing.T, program []byte) {
	t.Helper()
	h, _ := newWorktreeFaultSession(t)

	for _, args := range []map[string]any{
		{"operation": "create"},
		{"operation": "switch"},
		{"operation": "switch", "name": "lane", "path": h.root},
		{"operation": "remove"},
		{"operation": "not-an-operation"},
	} {
		if _, err := h.exec(args); err == nil {
			t.Fatalf("dispatch %v unexpectedly succeeded", args)
		}
	}

	child := filepath.Join(h.root, "nested", faultName(program, "child"))
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	if !pathEqualOrUnder(child, h.root) || !pathEqualOrUnder(h.root, h.root) {
		t.Fatalf("pathEqualOrUnder must accept child and equal paths")
	}
	if pathEqualOrUnder(h.root, child) {
		t.Fatalf("pathEqualOrUnder accepted parent %q under child %q", h.root, child)
	}
	if _, ok := relPathUnderManagedDir(filepath.Dir(h.managedPath("lane")), filepath.Dir(h.managedPath("lane"))); ok {
		t.Fatal("managed directory itself must not count as a managed worktree")
	}
	if got := metaDirForLane(filepath.Join(h.root, "lanes", "lane")); got != filepath.Join(h.root, "lanes", ".meta") {
		t.Fatalf("metaDirForLane = %q", got)
	}
	if got := shortSHA("0123456789abcdef"); got != "0123456789ab" {
		t.Fatalf("shortSHA = %q", got)
	}
	if got := shortSHA("short"); got != "short" {
		t.Fatalf("shortSHA preserved short value %q", got)
	}
	gitErr := &gitCmdError{code: 2, args: []string{"status", "--short"}, stderr: "bad repository"}
	if got := gitErr.Error(); !strings.Contains(got, "exit 2") || !strings.Contains(got, "bad repository") {
		t.Fatalf("gitCmdError text = %q", got)
	}
	if gitErr.ExitCode() != 2 {
		t.Fatalf("gitCmdError exit code = %d", gitErr.ExitCode())
	}
}

func worktreeFaultDelegateRollbackProgram(t *testing.T, program []byte) {
	t.Helper()
	h, _ := newWorktreeFaultSession(t)
	delegateID := faultName(program, "dlg")
	before := h.s.currentEnv().WorkingDirectory()

	path, branch, base, mainRoot, err := h.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	if branch != delegateID || base != "base-sha" || mainRoot != h.root {
		t.Fatalf("delegate creation result = (%q, %q, %q), want (%q, base-sha, %q)", branch, base, mainRoot, delegateID, h.root)
	}
	if got := h.s.currentEnv().WorkingDirectory(); got != before {
		t.Fatalf("delegate creation moved parent cwd to %q, want %q", got, before)
	}
	entry := h.git.entry(path)
	if entry == nil || entry.lockReason != worktree.FormatDelegateMarker(delegateID, h.s.id) {
		t.Fatalf("delegate entry lock = %#v", entry)
	}
	sc, err := worktree.ReadSidecar(h.metaDir(), delegateID)
	if err != nil {
		t.Fatalf("read delegate sidecar: %v", err)
	}
	if sc.DelegateID != delegateID || sc.CreatorSession != h.s.id || sc.BaseSHA != "base-sha" {
		t.Fatalf("delegate sidecar = %+v", sc)
	}

	h.s.rollbackFreshDelegateWorktree(delegateID, path)
	if h.git.entry(path) != nil {
		t.Fatalf("rollback left delegate entry %q", path)
	}
	if _, ok := h.git.branches[delegateID]; ok {
		t.Fatalf("rollback left delegate branch %q", delegateID)
	}
	h.requireNoSidecar(t, delegateID)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rollback left delegate path %q: %v", path, err)
	}
}

func worktreeFaultRestoreProgram(t *testing.T, program []byte) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)
	alphaName := faultName(program, "alpha")
	betaName := faultName(program, "beta")
	alpha := scriptedCreate(t, h, alphaName)
	beta := scriptedCreate(t, h, betaName)
	if _, err := h.exec(map[string]any{"operation": "switch", "name": alphaName}); err != nil {
		t.Fatalf("switch to restore target: %v", err)
	}

	// Model a session whose saved restore location is itself a managed lane.
	// The subsequent switch/exit use the real production lock choreography.
	restoreEnv := h.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	h.s.mu.Lock()
	h.s.worktreeRestoreEnv = restoreEnv
	h.s.mu.Unlock()
	if _, err := h.exec(map[string]any{"operation": "switch", "name": betaName}); err != nil {
		t.Fatalf("switch away from restore target: %v", err)
	}
	foreignReason := "serf:foreign-restore"
	h.setLock(alpha, foreignReason)
	out, err := h.exec(map[string]any{"operation": "exit"})
	if err != nil {
		t.Fatalf("exit to managed restore target: %v", err)
	}
	if warning, _ := out["warning"].(string); !strings.Contains(warning, foreignReason) {
		t.Fatalf("restore warning = %#v, want foreign lock reason %q", out["warning"], foreignReason)
	}
	if path, managed := h.currentState(); path != alpha || !managed {
		t.Fatalf("restore occupancy = (%q, %v), want (%q, true)", path, managed, alpha)
	}
	if entry := h.git.entry(beta); entry == nil || entry.lockReason != "" {
		t.Fatalf("left lane lock = %#v, want unlocked", entry)
	}

	h.setLock(alpha, "")
	if warning, err := h.s.relockRestoreTargetWithLockState(faults.run, alpha, false, ""); err != nil || warning != "" {
		t.Fatalf("relock unlocked restore target = (%q, %v)", warning, err)
	}
	h.requireOwnLock(t, alpha)
	if warning, err := h.s.relockRestoreTargetWithLockState(faults.run, alpha, true, worktree.FormatSessionMarker(h.s.id)); err != nil || warning != "" {
		t.Fatalf("adopt own restore lock = (%q, %v)", warning, err)
	}
	if warning, err := h.s.relockRestoreTargetWithLockState(faults.run, alpha, true, foreignReason); err != nil || !strings.Contains(warning, foreignReason) {
		t.Fatalf("foreign restore relock = (%q, %v)", warning, err)
	}
}

func worktreeFaultRemoveProgram(t *testing.T, program []byte) {
	t.Helper()

	t.Run("dirty current requires force_dirty", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		name := faultName(program, "dirty")
		path := scriptedCreate(t, h, name)
		faults.dirty[path] = "?? uncommitted.txt\n"
		if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
			t.Fatal("dirty current removal succeeded without force_dirty")
		}
		if got := h.s.currentEnv().WorkingDirectory(); got != path {
			t.Fatalf("dirty refusal moved cwd to %q, want %q", got, path)
		}
		out, err := h.exec(map[string]any{"operation": "remove", "name": name, "force_dirty": true, "delete_branch": true})
		if err != nil {
			t.Fatalf("forced dirty current removal: %v", err)
		}
		if out["branch_deleted"] != true || h.git.entry(path) != nil {
			t.Fatalf("forced dirty removal = %#v, entry=%#v", out, h.git.entry(path))
		}
		h.requireNoSidecar(t, name)
	})

	t.Run("own crash residue is released before remove", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		name := faultName(program, "residue")
		path := scriptedCreate(t, h, name)
		h.exitToRoot(t)
		h.setLock(path, worktree.FormatSessionMarker(h.s.id))
		out, err := h.exec(map[string]any{"operation": "remove", "name": name})
		if err != nil {
			t.Fatalf("remove own crash residue: %v", err)
		}
		if out["branch_deleted"] != false || h.git.entry(path) != nil {
			t.Fatalf("crash residue removal = %#v, entry=%#v", out, h.git.entry(path))
		}
		sc, err := worktree.ReadSidecar(h.metaDir(), name)
		if err != nil || !sc.WorktreeRemoved || sc.TipSHAAtRemoval != "base-sha" {
			t.Fatalf("residue sidecar = %+v, err=%v", sc, err)
		}
	})

	t.Run("foreign lock and live work refuse without removal", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		name := faultName(program, "guarded")
		path := scriptedCreate(t, h, name)
		h.exitToRoot(t)
		h.setLock(path, "serf:foreign-owner")
		if _, err := h.exec(map[string]any{"operation": "remove", "name": name, "force": true}); err == nil {
			t.Fatal("foreign-lock removal succeeded")
		}
		h.setLock(path, "")
		h.s.mu.Lock()
		h.s.worktreeLiveWorkStub = func(string) []string { return []string{"job-7"} }
		h.s.mu.Unlock()
		if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
			t.Fatal("live-work removal succeeded")
		}
		if h.git.entry(path) == nil {
			t.Fatal("guard refusal removed the lane")
		}
	})

	t.Run("unmerged branch is retained unless force is explicit", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		name := faultName(program, "unmerged")
		path := scriptedCreate(t, h, name)
		h.exitToRoot(t)
		h.git.entry(path).head = "tip-unmerged"
		h.git.branches[name] = "tip-unmerged"
		faults.mergeMode = worktreeFaultUnmerged
		out, err := h.exec(map[string]any{"operation": "remove", "name": name, "delete_branch": true})
		if err != nil {
			t.Fatalf("unmerged removal: %v", err)
		}
		if out["branch_deleted"] != false || !strings.Contains(fmt.Sprint(out["branch_kept_reason"]), "not merged") {
			t.Fatalf("unmerged branch disposition = %#v", out)
		}
		if _, ok := h.git.branches[name]; !ok {
			t.Fatal("unmerged branch was deleted without force")
		}

		forceName := faultName(program, "forced")
		forcePath := scriptedCreate(t, h, forceName)
		h.exitToRoot(t)
		h.git.entry(forcePath).head = "tip-forced"
		h.git.branches[forceName] = "tip-forced"
		out, err = h.exec(map[string]any{"operation": "remove", "name": forceName, "force": true, "delete_branch": true})
		if err != nil || out["branch_deleted"] != true {
			t.Fatalf("forced branch deletion = (%#v, %v)", out, err)
		}
	})
}

func worktreeFaultPruneSweepOneProgram(t *testing.T, program []byte) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)

	lockedName := faultName(program, "locked")
	lockedPath := scriptedCreate(t, h, lockedName)
	h.exitToRoot(t)
	h.setLock(lockedPath, "serf:foreign-prune")

	liveName := faultName(program, "live")
	livePath := scriptedCreate(t, h, liveName)
	h.exitToRoot(t)
	h.s.mu.Lock()
	h.s.worktreeLiveWorkStub = func(path string) []string {
		if filepath.Clean(path) == livePath {
			return []string{"delegate-live"}
		}
		return nil
	}
	h.s.mu.Unlock()

	missingName := faultName(program, "sidecarless")
	_ = scriptedCreate(t, h, missingName)
	h.exitToRoot(t)
	if err := worktree.DeleteSidecar(h.metaDir(), missingName); err != nil {
		t.Fatalf("delete sidecar fixture: %v", err)
	}

	dirtyName := faultName(program, "dirty")
	dirtyPath := scriptedCreate(t, h, dirtyName)
	h.exitToRoot(t)
	faults.dirty[dirtyPath] = " M edited.txt\n"

	statusName := faultName(program, "status")
	statusPath := scriptedCreate(t, h, statusName)
	h.exitToRoot(t)
	faults.statusErr[statusPath] = errors.New("scripted status failure")

	revName := faultName(program, "rev")
	revPath := scriptedCreate(t, h, revName)
	h.exitToRoot(t)
	faults.headErr[revPath] = errors.New("scripted head failure")

	unmergedName := faultName(program, "unmerged")
	unmergedPath := scriptedCreate(t, h, unmergedName)
	h.exitToRoot(t)
	h.git.entry(unmergedPath).head = "tip-prune-unmerged"
	h.git.branches[unmergedName] = "tip-prune-unmerged"
	faults.mergeMode = worktreeFaultUnmerged

	collectName := faultName(program, "collect")
	collectPath := scriptedCreate(t, h, collectName)
	h.exitToRoot(t)

	out, err := h.exec(map[string]any{"operation": "prune"})
	if err != nil {
		t.Fatalf("prune sweep one program: %v", err)
	}
	removed := worktreeFaultEntries(t, out, "removed")
	skipped := worktreeFaultEntries(t, out, "skipped")
	if !worktreeFaultEntryHas(removed, collectName, "unchanged") {
		t.Fatalf("prune did not collect unchanged lane: %#v", removed)
	}
	for _, want := range []struct{ name, reason string }{
		{lockedName, "locked"},
		{liveName, "live work"},
		{missingName, "sidecar-less"},
		{dirtyName, "dirty"},
		{statusName, "status check failed"},
		{revName, "rev-parse HEAD failed"},
		{unmergedName, "unmerged"},
	} {
		if !worktreeFaultEntryHas(skipped, want.name, want.reason) {
			t.Fatalf("prune did not retain %q with %q: %#v", want.name, want.reason, skipped)
		}
	}
	if h.git.entry(collectPath) != nil {
		t.Fatalf("collected lane remains in Git model: %q", collectPath)
	}
}

func worktreeFaultPruneSweepTwoProgram(t *testing.T, program []byte) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)

	inGrace := faultName(program, "grace")
	worktreeFaultWriteSidecar(t, h, inGrace, "base-sha", false, "", false)

	stale := faultName(program, "stale")
	worktreeFaultWriteSidecar(t, h, stale, "base-sha", false, "", true)

	adopted := faultName(program, "adopted")
	h.git.branches[adopted] = "tip-adopted"
	worktreeFaultWriteSidecar(t, h, adopted, "base-sha", true, "tip-before-adoption", true)

	disposable := faultName(program, "disposable")
	h.git.branches[disposable] = "base-sha"
	worktreeFaultWriteSidecar(t, h, disposable, "base-sha", true, "base-sha", true)

	unmerged := faultName(program, "unmerged")
	h.git.branches[unmerged] = "tip-sidecar-unmerged"
	worktreeFaultWriteSidecar(t, h, unmerged, "base-sha", false, "", true)
	faults.mergeMode = worktreeFaultUnmerged

	checkedOut := faultName(program, "checkedout")
	h.addExternal(checkedOut)
	worktreeFaultWriteSidecar(t, h, checkedOut, "base-sha", false, "", true)
	faults.deleteErr[checkedOut] = errors.New("branch is checked out")

	out, err := h.exec(map[string]any{"operation": "prune"})
	if err != nil {
		t.Fatalf("prune sweep two program: %v", err)
	}
	removed := worktreeFaultEntries(t, out, "removed")
	skipped := worktreeFaultEntries(t, out, "skipped")
	for _, want := range []struct{ name, reason string }{
		{stale, "stale sidecar"},
		{adopted, "adopted"},
		{disposable, "unchanged"},
	} {
		if !worktreeFaultEntryHas(removed, want.name, want.reason) {
			t.Fatalf("sweep two did not remove %q (%s): %#v", want.name, want.reason, removed)
		}
	}
	for _, want := range []struct{ name, reason string }{
		{inGrace, "in-grace"},
		{unmerged, "unmerged"},
		{checkedOut, "checked out at"},
	} {
		if !worktreeFaultEntryHas(skipped, want.name, want.reason) {
			t.Fatalf("sweep two did not skip %q (%s): %#v", want.name, want.reason, skipped)
		}
	}
	if _, ok := h.git.branches[disposable]; ok {
		t.Fatalf("disposable branch %q survived reconciliation", disposable)
	}
}

func worktreeFaultListAndRegistryProgram(t *testing.T, program []byte) {
	t.Helper()
	t.Run("best effort list fields", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		healthyName := faultName(program, "healthy")
		healthyPath := scriptedCreate(t, h, healthyName)
		h.exitToRoot(t)
		dirtyName := faultName(program, "listdirty")
		dirtyPath := scriptedCreate(t, h, dirtyName)
		h.exitToRoot(t)
		faults.dirty[dirtyPath] = "?? list-dirty.txt\n"
		faults.ahead[dirtyPath] = "not-an-int\n"
		if err := worktree.UpdateSidecar(h.metaDir(), dirtyName, func(sc *worktree.Sidecar) { sc.CreatedAt = "invalid-time" }); err != nil {
			t.Fatalf("rewrite sidecar: %v", err)
		}
		missingName := faultName(program, "missing")
		missingPath := scriptedCreate(t, h, missingName)
		h.exitToRoot(t)
		if err := os.RemoveAll(missingPath); err != nil {
			t.Fatalf("remove prunable fixture dir: %v", err)
		}
		out, err := h.exec(map[string]any{"operation": "list"})
		if err != nil {
			t.Fatalf("list best effort: %v", err)
		}
		entries := worktreeFaultEntries(t, out, "entries")
		if !worktreeFaultEntryHas(entries, healthyName, "") || !worktreeFaultEntryHas(entries, dirtyName, "") || !worktreeFaultEntryHas(entries, missingName, "") {
			t.Fatalf("list omitted known managed paths: %#v", entries)
		}
		if !strings.Contains(fmt.Sprint(out["message"]), healthyName) || !strings.Contains(fmt.Sprint(out["message"]), "dirty") {
			t.Fatalf("list summary = %#v", out["message"])
		}
		_ = healthyPath
	})

	t.Run("registry hygiene refuses foreign prunable entry", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		foreign := filepath.Join(h.root, "missing-foreign")
		faults.listAppend = fmt.Sprintf("worktree %s\nHEAD base-sha\nbranch refs/heads/foreign\nprunable missing\n\n", foreign)
		out, err := h.exec(map[string]any{"operation": "prune"})
		if err != nil {
			t.Fatalf("prune foreign registry entry: %v", err)
		}
		if out["registry_pruned"] != false || !strings.Contains(fmt.Sprint(out["registry_skip_reason"]), foreign) {
			t.Fatalf("registry skip result = %#v", out)
		}
	})

	t.Run("managed prunable entry permits registry cleanup", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		projectDir := filepath.Dir(h.managedPath("placeholder"))
		orphan := filepath.Join(projectDir, faultName(program, "orphan"))
		faults.listAppend = fmt.Sprintf("worktree %s\nHEAD base-sha\nbranch refs/heads/orphan\nprunable missing\n\n", orphan)
		out, err := h.exec(map[string]any{"operation": "prune"})
		if err != nil {
			t.Fatalf("prune managed registry entry: %v", err)
		}
		if out["registry_pruned"] != true {
			t.Fatalf("managed-only registry cleanup did not run: %#v", out)
		}
	})
}

func worktreeFaultSwitchProgram(t *testing.T, program []byte) {
	t.Helper()
	h, faults := newWorktreeFaultSession(t)
	name := faultName(program, "switch")
	path := scriptedCreate(t, h, name)
	h.exitToRoot(t)

	// A retained own marker represents crash residue and must be adopted without
	// relocking. The lock remains the session's marker after the switch.
	h.setLock(path, worktree.FormatSessionMarker(h.s.id))
	out, err := h.exec(map[string]any{"operation": "switch", "name": name})
	if err != nil || out["status"] != "switched" {
		t.Fatalf("switch adopting own lock = (%#v, %v)", out, err)
	}
	h.requireOwnLock(t, path)
	if out, err = h.exec(map[string]any{"operation": "switch", "name": name}); err != nil || out["status"] != "unchanged" {
		t.Fatalf("switch current no-op = (%#v, %v)", out, err)
	}

	foreign := h.addExternal(faultName(program, "external"))
	if out, err = h.exec(map[string]any{"operation": "switch", "path": foreign}); err != nil || out["status"] != "switched" {
		t.Fatalf("switch external path = (%#v, %v)", out, err)
	}
	if out, err = h.exec(map[string]any{"operation": "switch", "path": foreign}); err != nil || out["status"] != "unchanged" {
		t.Fatalf("switch external no-op = (%#v, %v)", out, err)
	}

	missing := filepath.Join(h.root, "does-not-exist")
	if _, err := h.exec(map[string]any{"operation": "switch", "path": missing}); err == nil {
		t.Fatal("missing path switch succeeded")
	}
	faults.listErr = errors.New("scripted porcelain failure")
	if _, err := h.exec(map[string]any{"operation": "switch", "name": name}); err == nil {
		t.Fatal("switch succeeded when porcelain inspection failed")
	}
}

func worktreeFaultCreateAndSwitchGuardsProgram(t *testing.T, program []byte) {
	t.Helper()
	t.Run("create validation and atomic cleanup", func(t *testing.T) {
		h, _ := newWorktreeFaultSession(t)
		name := faultName(program, "create")
		for _, baseRef := range []string{"-option", "bad ref", "does-not-exist"} {
			if _, err := h.exec(map[string]any{"operation": "create", "name": name, "base_ref": baseRef}); err == nil {
				t.Fatalf("create accepted invalid base_ref %q", baseRef)
			}
		}

		h.git.branches["already-there"] = "base-sha"
		if _, err := h.exec(map[string]any{"operation": "create", "name": "already-there"}); err == nil {
			t.Fatal("create accepted an existing unmanaged branch")
		}

		sidecarName := faultName(program, "sidecar")
		worktreeFaultWriteSidecar(t, h, sidecarName, "base-sha", false, "", false)
		if _, err := h.exec(map[string]any{"operation": "create", "name": sidecarName}); err == nil {
			t.Fatal("create overwrote a racing sidecar")
		}

		failedName := faultName(program, "addfail")
		h.git.failNextAdd = true
		if _, err := h.exec(map[string]any{"operation": "create", "name": failedName}); err == nil {
			t.Fatal("injected atomic add failure succeeded")
		}
		h.requireNoSidecar(t, failedName)
	})

	t.Run("detached creation records no merge target", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		faults.symbolicRefErr[h.root] = errors.New("scripted detached HEAD")
		name := faultName(program, "detached")
		path := scriptedCreate(t, h, name)
		sc, err := worktree.ReadSidecar(h.metaDir(), name)
		if err != nil {
			t.Fatalf("read detached sidecar: %v", err)
		}
		if sc.MergeTarget != "" {
			t.Fatalf("detached creation merge target = %q, want empty", sc.MergeTarget)
		}
		if path == "" {
			t.Fatal("detached creation returned an empty path")
		}
	})

	t.Run("switch failures retain the prior state", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		name := faultName(program, "switchguard")
		path := scriptedCreate(t, h, name)
		if _, err := h.s.switchToCurrentNoOp(faults.run, path, false, ""); err == nil {
			t.Fatal("current switch accepted an unlocked lane")
		}
		h.exitToRoot(t)

		faults.lockErr[path] = errors.New("scripted lock failure")
		if _, err := h.exec(map[string]any{"operation": "switch", "name": name}); err == nil {
			t.Fatal("switch succeeded when target lock failed")
		}
		delete(faults.lockErr, path)
		if got := h.s.currentEnv().WorkingDirectory(); got != h.root {
			t.Fatalf("failed target lock moved cwd to %q, want root %q", got, h.root)
		}

		if err := os.Remove(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("remove .git pointer: %v", err)
		}
		if _, err := h.exec(map[string]any{"operation": "switch", "name": name}); err == nil {
			t.Fatal("switch accepted a registered path without a .git pointer")
		}

		unknown := t.TempDir()
		if _, err := h.exec(map[string]any{"operation": "switch", "path": unknown}); err == nil {
			t.Fatal("switch accepted an existing but unregistered path")
		}
		if _, err := h.exec(map[string]any{"operation": "switch", "name": "bad name"}); err == nil {
			t.Fatal("switch accepted an invalid managed name")
		}
	})
}

func worktreeFaultOperationErrorsProgram(t *testing.T, program []byte) {
	t.Helper()
	t.Run("not-a-repository guards", func(t *testing.T) {
		s := newSession(t, withDir(t.TempDir()))
		if _, err := s.worktreeRemove(context.Background(), "lane", false, false, false); err == nil {
			t.Fatal("remove outside a repository succeeded")
		}
		if _, err := s.worktreeList(context.Background()); err == nil {
			t.Fatal("list outside a repository succeeded")
		}
		if _, err := s.worktreePrune(context.Background()); err == nil {
			t.Fatal("prune outside a repository succeeded")
		}
	})

	t.Run("remove error branches preserve the lane", func(t *testing.T) {
		t.Run("malformed sidecar", func(t *testing.T) {
			h, _ := newWorktreeFaultSession(t)
			name := faultName(program, "malformed")
			path := scriptedCreate(t, h, name)
			h.exitToRoot(t)
			sidecarPath := filepath.Join(h.metaDir(), worktree.EncodeSidecarName(name)+".json")
			if err := os.WriteFile(sidecarPath, []byte("{"), 0o644); err != nil {
				t.Fatalf("corrupt sidecar: %v", err)
			}
			if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
				t.Fatal("remove accepted malformed metadata")
			}
			if h.git.entry(path) == nil {
				t.Fatal("malformed metadata refusal removed the lane")
			}
		})

		t.Run("status and remove runner failures", func(t *testing.T) {
			h, faults := newWorktreeFaultSession(t)
			name := faultName(program, "statuserr")
			path := scriptedCreate(t, h, name)
			h.exitToRoot(t)
			faults.statusErr[path] = errors.New("scripted status failure")
			if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
				t.Fatal("remove accepted a failed dirty-tree check")
			}
			delete(faults.statusErr, path)
			faults.removeErr[path] = errors.New("scripted remove failure")
			if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
				t.Fatal("remove accepted a failed git worktree remove")
			}
			if h.git.entry(path) == nil {
				t.Fatal("remove failure lost the lane")
			}
		})

		t.Run("current lane without restore is refused", func(t *testing.T) {
			h, _ := newWorktreeFaultSession(t)
			name := faultName(program, "no-restore")
			path := scriptedCreate(t, h, name)
			h.s.mu.Lock()
			h.s.worktreeRestoreEnv = nil
			h.s.mu.Unlock()
			if _, err := h.exec(map[string]any{"operation": "remove", "name": name}); err == nil {
				t.Fatal("remove accepted its active root with no safe restore")
			}
			if h.git.entry(path) == nil {
				t.Fatal("no-restore refusal removed the active lane")
			}
		})

		t.Run("branch lookup and merge evidence", func(t *testing.T) {
			h, faults := newWorktreeFaultSession(t)
			missingName := faultName(program, "missingref")
			missingPath := scriptedCreate(t, h, missingName)
			h.exitToRoot(t)
			faults.refErr[missingName] = errors.New("scripted missing branch")
			out, err := h.exec(map[string]any{"operation": "remove", "name": missingName, "delete_branch": true})
			if err != nil || !strings.Contains(fmt.Sprint(out["branch_kept_reason"]), "not found") {
				t.Fatalf("missing branch disposition = (%#v, %v)", out, err)
			}
			if h.git.entry(missingPath) != nil {
				t.Fatal("removed worktree remained after a branch lookup failure")
			}

			mergeName := faultName(program, "mergeerr")
			mergePath := scriptedCreate(t, h, mergeName)
			h.exitToRoot(t)
			h.git.entry(mergePath).head = "tip-merge-error"
			h.git.branches[mergeName] = "tip-merge-error"
			faults.mergeMode = worktreeFaultMergeError
			if _, err := h.exec(map[string]any{"operation": "remove", "name": mergeName, "delete_branch": true}); err == nil {
				t.Fatal("remove accepted a merge predicate failure")
			}
		})
	})

	t.Run("prune and predicate error branches", func(t *testing.T) {
		h, faults := newWorktreeFaultSession(t)
		if _, err := h.exec(map[string]any{"operation": "prune"}); err != nil {
			t.Fatalf("empty metadata prune: %v", err)
		}

		faults.listErr = errors.New("scripted list failure")
		if _, _, err := worktreePruneSweep3(faults.run, filepath.Dir(h.managedPath("placeholder"))); err == nil {
			t.Fatal("sweep three accepted a failed registry list")
		}
		faults.listErr = nil
		faults.pruneErr = errors.New("scripted prune failure")
		if _, _, err := worktreePruneSweep3(faults.run, filepath.Dir(h.managedPath("placeholder"))); err == nil {
			t.Fatal("sweep three accepted a failed git worktree prune")
		}

		if disposable, reason, err := disposableReason(faults.run, "tip", "base", ""); err != nil || disposable || reason != "merge target unknown" {
			t.Fatalf("unknown-target disposal = (%v, %q, %v)", disposable, reason, err)
		}
		if disposable, reason, err := disposableReason(faults.run, "tip", "base", "main"); err != nil || !disposable || reason != "merged (ancestry)" {
			t.Fatalf("ancestry disposal = (%v, %q, %v)", disposable, reason, err)
		}
		faults.mergeMode = worktreeFaultMergeError
		if _, _, err := disposableReason(faults.run, "tip", "base", "main"); err == nil {
			t.Fatal("disposableReason accepted a failed merge predicate")
		}
	})
}

func faultName(program []byte, prefix string) string {
	b := byte(0)
	if len(program) > 1 {
		b = program[1]
	}
	return fmt.Sprintf("%s-%02x", prefix, b)
}

type worktreeFaultMergeMode uint8

const (
	worktreeFaultMerged worktreeFaultMergeMode = iota
	worktreeFaultUnmerged
	worktreeFaultCherry
	worktreeFaultMergeError
)

type worktreeFaults struct {
	git *scriptedWorktreeGit

	dirty     map[string]string
	statusErr map[string]error
	headErr   map[string]error
	ahead     map[string]string
	deleteErr map[string]error
	lockErr   map[string]error
	removeErr map[string]error
	refErr    map[string]error

	symbolicRefErr map[string]error
	pruneErr       error

	listErr    error
	listAppend string
	mergeMode  worktreeFaultMergeMode
}

func newWorktreeFaultSession(t *testing.T) (*scriptedWorktreeSession, *worktreeFaults) {
	t.Helper()
	h := newScriptedWorktreeSession(t)
	faults := &worktreeFaults{
		git:       h.git,
		dirty:     make(map[string]string),
		statusErr: make(map[string]error),
		headErr:   make(map[string]error),
		ahead:     make(map[string]string),
		deleteErr: make(map[string]error),
		lockErr:   make(map[string]error),
		removeErr: make(map[string]error),
		refErr:    make(map[string]error),

		symbolicRefErr: make(map[string]error),
	}
	h.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return faults.run
	}
	return h, faults
}

func (f *worktreeFaults) run(args ...string) (string, error) {
	if scriptedArgs(args, "worktree", "list", "--porcelain") {
		f.record(args)
		if f.listErr != nil {
			return "", f.listErr
		}
		return f.git.porcelain() + f.listAppend, nil
	}
	if len(args) >= 4 && args[0] == "worktree" && args[1] == "remove" {
		path := filepath.Clean(args[len(args)-1])
		if err := f.removeErr[path]; err != nil {
			f.record(args)
			return "", err
		}
	}
	if scriptedArgs(args, "worktree", "prune") && f.pruneErr != nil {
		f.record(args)
		return "", f.pruneErr
	}
	if len(args) == 5 && args[0] == "worktree" && args[1] == "lock" && args[2] == "--reason" {
		path := filepath.Clean(args[4])
		if err := f.lockErr[path]; err != nil {
			f.record(args)
			return "", err
		}
	}
	if len(args) == 3 && args[0] == "branch" && args[1] == "-D" {
		if err := f.deleteErr[args[2]]; err != nil {
			f.record(args)
			return "", err
		}
	}
	if len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" {
		name := strings.TrimPrefix(args[2], "refs/heads/")
		if err := f.refErr[name]; err != nil {
			f.record(args)
			return "", err
		}
	}
	if len(args) >= 4 && args[0] == "-C" {
		path := filepath.Clean(args[1])
		if len(args) == 6 && args[2] == "symbolic-ref" && args[3] == "--quiet" && args[4] == "--short" && args[5] == "HEAD" {
			if err := f.symbolicRefErr[path]; err != nil {
				f.record(args)
				return "", err
			}
		}
		if len(args) == 5 && args[2] == "status" && args[3] == "--porcelain=v1" && args[4] == "--untracked-files=all" {
			if err := f.statusErr[path]; err != nil {
				f.record(args)
				return "", err
			}
			if out, ok := f.dirty[path]; ok {
				f.record(args)
				return out, nil
			}
		}
		if len(args) == 4 && args[2] == "rev-parse" && args[3] == "HEAD" {
			if err := f.headErr[path]; err != nil {
				f.record(args)
				return "", err
			}
		}
		if len(args) == 5 && args[2] == "rev-list" && args[3] == "--count" {
			if out, ok := f.ahead[path]; ok {
				f.record(args)
				return out, nil
			}
		}
	}
	if len(args) == 4 && args[0] == "merge-base" && args[1] == "--is-ancestor" {
		switch f.mergeMode {
		case worktreeFaultUnmerged, worktreeFaultCherry:
			f.record(args)
			return "", &gitCmdError{code: 1, args: append([]string(nil), args...), stderr: "not an ancestor"}
		case worktreeFaultMergeError:
			f.record(args)
			return "", errors.New("scripted merge-base failure")
		}
	}
	if len(args) == 4 && args[0] == "cherry" {
		switch f.mergeMode {
		case worktreeFaultUnmerged:
			f.record(args)
			return "+ unmatched\n", nil
		case worktreeFaultCherry:
			f.record(args)
			return "- equivalent\n", nil
		case worktreeFaultMergeError:
			f.record(args)
			return "", errors.New("scripted cherry failure")
		}
	}
	return f.git.run(args...)
}

func (f *worktreeFaults) record(args []string) {
	f.git.calls = append(f.git.calls, append([]string(nil), args...))
}

func worktreeFaultWriteSidecar(t *testing.T, h *scriptedWorktreeSession, name, base string, removed bool, tipAtRemoval string, old bool) {
	t.Helper()
	metaDir := h.metaDir()
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir sidecar metadata: %v", err)
	}
	sc := worktree.Sidecar{
		Name:            name,
		Branch:          name,
		BaseSHA:         base,
		MergeTarget:     "main",
		OriginalRoot:    h.root,
		CreatorSession:  h.s.id,
		WorktreeRemoved: removed,
		TipSHAAtRemoval: tipAtRemoval,
		CreatedAt:       "2026-01-01T00:00:00Z",
	}
	if err := worktree.WriteSidecarExcl(metaDir, name, sc); err != nil {
		t.Fatalf("write sidecar %q: %v", name, err)
	}
	if old {
		path := filepath.Join(metaDir, worktree.EncodeSidecarName(name)+".json")
		then := time.Now().Add(-worktree.ReconcileGrace - time.Second)
		if err := os.Chtimes(path, then, then); err != nil {
			t.Fatalf("age sidecar %q: %v", name, err)
		}
	}
}

func worktreeFaultEntries(t *testing.T, out map[string]any, key string) []map[string]any {
	t.Helper()
	entries, ok := out[key].([]map[string]any)
	if !ok {
		t.Fatalf("%s entries = %T, want []map[string]any", key, out[key])
	}
	return entries
}

func worktreeFaultEntryHas(entries []map[string]any, name, reasonPart string) bool {
	for _, entry := range entries {
		if entry["name"] != name {
			continue
		}
		return reasonPart == "" || strings.Contains(fmt.Sprint(entry["reason"]), reasonPart)
	}
	return false
}
