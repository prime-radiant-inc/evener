package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for the manage_worktree list and prune
// arms (spec §5 list steps 1-3; prune sweeps 1-3). They reuse
// wtRepo/wtGit/newWorktreeRepo/wtLaunchSession from
// session_tools_worktree_create_test.go and session_tools_worktree_switch_test.go.

// listOp drives the list operation through the registered tool surface.
func (r *wtRepo) listOp(t *testing.T) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "list"})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("list result is %T, want map[string]any", out)
	}
	return m, nil
}

// pruneOp drives the prune operation through the registered tool surface.
func (r *wtRepo) pruneOp(t *testing.T) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "prune"})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("prune result is %T, want map[string]any", out)
	}
	return m, nil
}

// listEntries extracts the "entries" slice from a list result as
// []map[string]any, failing the test on a shape mismatch.
func listEntries(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, ok := out["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("entries is %T, want []map[string]any", out["entries"])
	}
	return raw
}

// findEntry locates the entry named name, failing the test if absent.
func findEntry(t *testing.T, entries []map[string]any, name string) map[string]any {
	t.Helper()
	for _, e := range entries {
		if e["name"] == name {
			return e
		}
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = fmt.Sprint(e["name"])
	}
	t.Fatalf("no entry named %q; have %v", name, names)
	return nil
}

// pruneEntries extracts "removed" or "skipped" from a prune result.
func pruneEntries(t *testing.T, out map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := out[key].([]map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want []map[string]any", key, out[key])
	}
	return raw
}

// findPruneEntry locates a prune-report entry by name across removed/skipped
// lists, failing the test if absent.
func findPruneEntry(t *testing.T, entries []map[string]any, name string) map[string]any {
	t.Helper()
	for _, e := range entries {
		if e["name"] == name {
			return e
		}
	}
	return nil
}

// commitInWorktree writes name/content and commits it inside dir, returning
// the new HEAD SHA.
func commitInWorktree(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	wtGit(t, dir, "config", "user.email", "test@example.com")
	wtGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	wtGit(t, dir, "add", name)
	wtGit(t, dir, "commit", "-m", msg)
	return strings.TrimSpace(wtGit(t, dir, "rev-parse", "HEAD"))
}

// ageSidecar backdates name's sidecar mtime by age, so prune sweep 2's grace
// check (mtime-judged, spec §5) treats it as no longer fresh.
func ageSidecar(t *testing.T, metaDir, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(metaDir, worktree.EncodeSidecarName(name)+".json")
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// ============================================================
// list
// ============================================================

func TestWorktreeList_DoesNotPrune(t *testing.T) {
	r := newWorktreeRepo(t)

	// A non-managed sibling worktree, created directly with git (never
	// touched by manage_worktree), whose directory then goes missing —
	// exactly the "sibling lane on an unmounted volume" case spec §5 list
	// step 1 says list must never deregister.
	siblingRoot := t.TempDir()
	siblingPath := filepath.Join(siblingRoot, "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling-branch", siblingPath, r.head)
	if err := os.RemoveAll(siblingPath); err != nil {
		t.Fatalf("remove sibling dir: %v", err)
	}

	before := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	foundBefore := false
	for _, e := range worktree.ParsePorcelain(before) {
		if filepath.Clean(e.Path) == filepath.Clean(siblingPath) {
			foundBefore = true
			if !e.Prunable {
				t.Fatal("sibling entry should be prunable once its directory is gone")
			}
		}
	}
	if !foundBefore {
		t.Fatal("sibling entry missing before list even ran")
	}

	if _, err := r.listOp(t); err != nil {
		t.Fatalf("list: %v", err)
	}

	after := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	foundAfter := false
	for _, e := range worktree.ParsePorcelain(after) {
		if filepath.Clean(e.Path) == filepath.Clean(siblingPath) {
			foundAfter = true
		}
	}
	if !foundAfter {
		t.Fatal("list deregistered the non-managed prunable sibling; list must never run `git worktree prune`")
	}
}

func TestWorktreeList_StalenessFieldsThreeWorktreeFixture(t *testing.T) {
	r := newWorktreeRepo(t)

	// 1: unchanged — created, never touched.
	if _, err := r.create(t, map[string]any{"name": "unchanged"}); err != nil {
		t.Fatalf("create unchanged: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// 2: dirty + ahead — one commit, plus an uncommitted file.
	resDirty, err := r.create(t, map[string]any{"name": "dirty-ahead"})
	if err != nil {
		t.Fatalf("create dirty-ahead: %v", err)
	}
	pathDirty := resDirty["path"].(string)
	commitInWorktree(t, pathDirty, "a.txt", "a\n", "advance dirty-ahead")
	if err := os.WriteFile(filepath.Join(pathDirty, "b.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// 3: merged — one commit, ff-merged to main.
	resMerged, err := r.create(t, map[string]any{"name": "merged-lane"})
	if err != nil {
		t.Fatalf("create merged-lane: %v", err)
	}
	pathMerged := resMerged["path"].(string)
	commitInWorktree(t, pathMerged, "m.txt", "m\n", "advance merged-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	wtGit(t, r.mainRoot, "merge", "--ff-only", "merged-lane")

	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries := listEntries(t, out)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}

	eUnchanged := findEntry(t, entries, "unchanged")
	if eUnchanged["dirty"] != false {
		t.Errorf("unchanged.dirty = %v, want false", eUnchanged["dirty"])
	}
	if eUnchanged["ahead_commits"] != 0 {
		t.Errorf("unchanged.ahead_commits = %v, want 0", eUnchanged["ahead_commits"])
	}
	if eUnchanged["creator_session"] != r.s.id {
		t.Errorf("unchanged.creator_session = %v, want %s", eUnchanged["creator_session"], r.s.id)
	}
	if eUnchanged["locked"] != false {
		t.Errorf("unchanged.locked = %v, want false (exited)", eUnchanged["locked"])
	}

	eDirty := findEntry(t, entries, "dirty-ahead")
	if eDirty["dirty"] != true {
		t.Errorf("dirty-ahead.dirty = %v, want true", eDirty["dirty"])
	}
	if eDirty["ahead_commits"] != 1 {
		t.Errorf("dirty-ahead.ahead_commits = %v, want 1", eDirty["ahead_commits"])
	}
	if eDirty["merged"] != false {
		t.Errorf("dirty-ahead.merged = %v, want false (main never advanced)", eDirty["merged"])
	}

	eMerged := findEntry(t, entries, "merged-lane")
	if eMerged["dirty"] != false {
		t.Errorf("merged-lane.dirty = %v, want false", eMerged["dirty"])
	}
	if eMerged["ahead_commits"] != 1 {
		t.Errorf("merged-lane.ahead_commits = %v, want 1", eMerged["ahead_commits"])
	}
	if eMerged["merged"] != true {
		t.Errorf("merged-lane.merged = %v, want true", eMerged["merged"])
	}
	if eMerged["merged_arm"] != "ancestry" {
		t.Errorf("merged-lane.merged_arm = %v, want ancestry", eMerged["merged_arm"])
	}

	for _, e := range entries {
		age, ok := e["age_seconds"].(float64)
		if !ok || age < 0 || age > 60 {
			t.Errorf("%v.age_seconds = %v, want a small non-negative number", e["name"], e["age_seconds"])
		}
		if e["has_metadata"] != true {
			t.Errorf("%v.has_metadata = %v, want true", e["name"], e["has_metadata"])
		}
	}
}

func TestWorktreeList_PrefixCollisionFiltering(t *testing.T) {
	r := newWorktreeRepo(t)
	if _, err := r.create(t, map[string]any{"name": "real-lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	canonicalMain := r.canonicalMain(t)
	realProjectDir := filepath.Join(r.stateDir, "worktrees", worktree.ProjectID(canonicalMain))
	// A sibling directory whose name has realProjectDir's basename as a
	// literal STRING PREFIX (spec §5 list step 2: "not bare HasPrefix, which
	// collides when one projectid prefixes another").
	collidingProjectDir := realProjectDir + "-COLLIDE"
	collidingPath := filepath.Join(collidingProjectDir, "intruder")
	if err := os.MkdirAll(filepath.Dir(collidingPath), 0o755); err != nil {
		t.Fatalf("mkdir colliding parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "intruder-branch", collidingPath, r.head)

	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries := listEntries(t, out)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (intruder must be filtered out): %+v", len(entries), entries)
	}
	if entries[0]["name"] != "real-lane" {
		t.Errorf("entries[0].name = %v, want real-lane", entries[0]["name"])
	}
}

func TestWorktreeList_SymlinkedWorktreeRootCanonicalization(t *testing.T) {
	r := newWorktreeRepo(t)
	realStateDir := r.stateDir
	linkParent := t.TempDir()
	symlinkedStateDir := filepath.Join(linkParent, "state-link")
	if err := os.Symlink(realStateDir, symlinkedStateDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r.s.stateDir = symlinkedStateDir

	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries := listEntries(t, out)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	e := findEntry(t, entries, "lane")
	canonicalPath, evErr := filepath.EvalSymlinks(res["path"].(string))
	if evErr != nil {
		t.Fatalf("EvalSymlinks result path: %v", evErr)
	}
	gotPath, _ := e["path"].(string)
	gotCanonical, evErr2 := filepath.EvalSymlinks(gotPath)
	if evErr2 != nil {
		t.Fatalf("EvalSymlinks entry path %q: %v", gotPath, evErr2)
	}
	if gotCanonical != canonicalPath {
		t.Errorf("entry path canonicalizes to %q, want %q", gotCanonical, canonicalPath)
	}
}

// ============================================================
// prune — sweep 1 (registered managed worktrees)
// ============================================================

func TestWorktreePrune_Sweep1_CollectsUnchanged(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "unchanged"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	removed := pruneEntries(t, out, "removed")
	e := findPruneEntry(t, removed, "unchanged")
	if e == nil {
		t.Fatalf("unchanged not in removed: %+v", removed)
	}
	if e["reason"] != "unchanged" {
		t.Errorf("reason = %v, want unchanged", e["reason"])
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir survived prune: err=%v", statErr)
	}
	if branchExistsInRepo(t, r.mainRoot, "unchanged") {
		t.Error("branch survived prune of an unchanged lane")
	}
	canonicalMain := r.canonicalMain(t)
	if _, scErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "unchanged"); !os.IsNotExist(scErr) {
		t.Errorf("sidecar survived: err=%v", scErr)
	}
}

func TestWorktreePrune_Sweep1_CollectsMergedAncestry(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "anc-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "a.txt", "a\n", "advance anc-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	wtGit(t, r.mainRoot, "merge", "--ff-only", "anc-lane")

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), "anc-lane")
	if e == nil {
		t.Fatal("anc-lane not removed")
	}
	if e["reason"] != "merged (ancestry)" {
		t.Errorf("reason = %v, want merged (ancestry)", e["reason"])
	}
	if branchExistsInRepo(t, r.mainRoot, "anc-lane") {
		t.Error("branch survived")
	}
}

func TestWorktreePrune_Sweep1_CollectsMergedCherry(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "squash-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "s.txt", "s\n", "advance squash-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// Squash-merge: content lands on main as a NEW commit, so ancestry
	// cannot recognize it; only patch-equivalence (`git cherry`) can.
	wtGit(t, r.mainRoot, "merge", "--squash", "squash-lane")
	wtGit(t, r.mainRoot, "commit", "-m", "squash squash-lane")

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), "squash-lane")
	if e == nil {
		t.Fatal("squash-lane not removed")
	}
	if e["reason"] != "merged (cherry)" {
		t.Errorf("reason = %v, want merged (cherry)", e["reason"])
	}
}

func TestWorktreePrune_Sweep1_SkipsLocked(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Session stays INSIDE lane: it is locked with the session's own marker.

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "lane")
	if e == nil {
		t.Fatal("lane not reported skipped")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want it to mention locked", reason)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("locked worktree removed by prune: %v", statErr)
	}
}

func TestWorktreePrune_Sweep1_SkipsDirty(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "dirty-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, "d.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write d.txt: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "dirty-lane")
	if e == nil {
		t.Fatal("dirty-lane not reported skipped")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "dirty") {
		t.Errorf("reason = %q, want it to mention dirty", reason)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("dirty worktree removed by prune: %v", statErr)
	}
}

func TestWorktreePrune_Sweep1_SkipsLiveViaStub(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "live-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	r.s.worktreeLiveWorkStub = func(p string) []string {
		if p == filepath.Clean(path) {
			return []string{"job_live999 (shell, running)"}
		}
		return nil
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "live-lane")
	if e == nil {
		t.Fatal("live-lane not reported skipped")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "job_live999") {
		t.Errorf("reason = %q, want it to surface the live-work evidence", reason)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("live-work worktree removed by prune: %v", statErr)
	}

	r.s.worktreeLiveWorkStub = nil
	out2, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune after clearing stub: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out2, "removed"), "live-lane") == nil {
		t.Fatal("live-lane not collected once live-work stub cleared")
	}
}

func TestWorktreePrune_Sweep1_SkipsSidecarLess(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	path := r.managedPath(canonicalMain, "unmanaged-lane")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// A worktree placed directly under the managed dir by hand (no sidecar) —
	// provenance unknown, not serf's to judge.
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "unmanaged-lane", path, r.head)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "unmanaged-lane")
	if e == nil {
		t.Fatal("unmanaged-lane not reported skipped")
	}
	if e["reason"] != "sidecar-less" {
		t.Errorf("reason = %v, want sidecar-less", e["reason"])
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("sidecar-less worktree removed by prune: %v", statErr)
	}
}

func TestWorktreePrune_Sweep1_SkipsUnmerged(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "unmerged-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "u.txt", "u\n", "advance unmerged-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// main is NOT advanced: unmerged-lane is never merged.

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "unmerged-lane")
	if e == nil {
		t.Fatal("unmerged-lane not reported skipped")
	}
	if e["reason"] != "unmerged" {
		t.Errorf("reason = %v, want unmerged", e["reason"])
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("unmerged worktree removed by prune: %v", statErr)
	}
	if !branchExistsInRepo(t, r.mainRoot, "unmerged-lane") {
		t.Error("branch removed despite being unmerged")
	}
}

func TestWorktreePrune_Sweep1_SkipsMergeTargetUnknown(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "orphan-target-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Advance the tip so it differs from base_sha: disposableReason must reach
	// the merge check rather than short-circuiting on the unchanged arm.
	commitInWorktree(t, path, "o.txt", "o\n", "advance orphan-target-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// The sidecar recorded merge_target="main" (the branch checked out at the
	// active root when the lane was created). Rename it away so that, at
	// prune time, neither refs/heads/main nor any refs/remotes/*/main
	// resolves — worktree.Merged's TargetUnknown path (spec §5).
	wtGit(t, r.mainRoot, "branch", "-m", "main", "main-renamed")

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "orphan-target-lane")
	if e == nil {
		t.Fatal("orphan-target-lane not reported skipped")
	}
	if e["reason"] != "merge target unknown" {
		t.Errorf("reason = %v, want merge target unknown", e["reason"])
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("worktree removed despite an unresolvable merge target: %v", statErr)
	}
	if !branchExistsInRepo(t, r.mainRoot, "orphan-target-lane") {
		t.Error("branch removed despite an unresolvable merge target")
	}
}

// ============================================================
// prune — sweep 2 (sidecar reconciliation)
// ============================================================

func TestWorktreePrune_Sweep2_StaleSidecarDeletedPostGrace(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metaDir: %v", err)
	}
	sc := worktree.Sidecar{
		Name: "ghost", Branch: "ghost", BaseSHA: r.head,
		OriginalRoot: canonicalMain, CreatorSession: r.s.id,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := worktree.WriteSidecarExcl(metaDir, "ghost", sc); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	ageSidecar(t, metaDir, "ghost", worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), "ghost")
	if e == nil {
		t.Fatal("ghost not reported removed")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "stale sidecar") {
		t.Errorf("reason = %q, want it to mention stale sidecar", reason)
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "ghost"); !os.IsNotExist(scErr) {
		t.Errorf("stale sidecar survived: err=%v", scErr)
	}
}

func TestWorktreePrune_Sweep2_FreshSidecarSurvivesGrace(t *testing.T) {
	r := newWorktreeRepo(t)
	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metaDir: %v", err)
	}
	sc := worktree.Sidecar{
		Name: "racing", Branch: "racing", BaseSHA: r.head,
		OriginalRoot: canonicalMain, CreatorSession: r.s.id,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := worktree.WriteSidecarExcl(metaDir, "racing", sc); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	// No aging: mtime is fresh (a concurrent create's sidecar, still in
	// grace — spec §5 sweep 2).

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "racing")
	if e == nil {
		t.Fatal("racing not reported skipped")
	}
	if e["reason"] != "in-grace" {
		t.Errorf("reason = %v, want in-grace", e["reason"])
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "racing"); scErr != nil {
		t.Errorf("fresh sidecar deleted despite grace: %v", scErr)
	}
}

func TestWorktreePrune_Sweep2_AdoptedBranchSidecarDroppedBranchKept(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "adopt-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	laneTip := commitInWorktree(t, path, "a.txt", "a\n", "advance adopt-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}

	if _, err := r.removeOp(t, map[string]any{"name": "adopt-lane"}); err != nil {
		t.Fatalf("remove (branch kept): %v", err)
	}

	// The user builds on the kept branch after removal: a new commit whose
	// tip is neither base_sha nor the recorded tip_sha_at_removal.
	adoptedTip := commitOnBranch(t, r.mainRoot, "adopt-lane", "b.txt", "b\n", "user adopted this branch")
	if adoptedTip == laneTip {
		t.Fatal("adoptedTip did not advance")
	}

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	ageSidecar(t, metaDir, "adopt-lane", worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), "adopt-lane")
	if e == nil {
		t.Fatal("adopt-lane not reported removed (sidecar drop)")
	}
	if e["reason"] != "adopted" {
		t.Errorf("reason = %v, want adopted", e["reason"])
	}
	if e["branch_removed"] != false {
		t.Errorf("branch_removed = %v, want false (adopted branch is kept)", e["branch_removed"])
	}
	if e["sidecar_removed"] != true {
		t.Errorf("sidecar_removed = %v, want true", e["sidecar_removed"])
	}
	if !branchExistsInRepo(t, r.mainRoot, "adopt-lane") {
		t.Error("adopted branch was deleted; it must be kept")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "adopt-lane"); !os.IsNotExist(scErr) {
		t.Errorf("sidecar survived an adopted branch: err=%v", scErr)
	}
	gotTip := strings.TrimSpace(wtGit(t, r.mainRoot, "rev-parse", "refs/heads/adopt-lane"))
	if gotTip != adoptedTip {
		t.Errorf("branch tip = %s, want unchanged %s (adopted branch must not move)", gotTip, adoptedTip)
	}
}

func TestWorktreePrune_Sweep2_ResetToBaseCollected(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "reset-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "a.txt", "a\n", "advance reset-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if _, err := r.removeOp(t, map[string]any{"name": "reset-lane"}); err != nil {
		t.Fatalf("remove (branch kept): %v", err)
	}

	// Branch reset back to base_sha: NOT adopted (spec §5's two-SHA rule) —
	// collectible via the unchanged arm exactly as if nothing was committed.
	wtGit(t, r.mainRoot, "update-ref", "refs/heads/reset-lane", r.head)

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	ageSidecar(t, metaDir, "reset-lane", worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), "reset-lane")
	if e == nil {
		t.Fatal("reset-lane not reported removed")
	}
	if e["reason"] != "unchanged" {
		t.Errorf("reason = %v, want unchanged (not adopted)", e["reason"])
	}
	if branchExistsInRepo(t, r.mainRoot, "reset-lane") {
		t.Error("branch survived; a reset-to-base branch must be collected")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "reset-lane"); !os.IsNotExist(scErr) {
		t.Errorf("sidecar survived: err=%v", scErr)
	}
}

func TestWorktreePrune_Sweep2_CheckedOutBranchSkipped(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "checkedout-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "a.txt", "a\n", "advance checkedout-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	wtGit(t, r.mainRoot, "merge", "--ff-only", "checkedout-lane")

	if _, err := r.removeOp(t, map[string]any{"name": "checkedout-lane"}); err != nil {
		t.Fatalf("remove (branch kept): %v", err)
	}

	otherPath := filepath.Join(t.TempDir(), "other-checkout")
	wtGit(t, r.mainRoot, "worktree", "add", "--force", otherPath, "checkedout-lane")

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	ageSidecar(t, metaDir, "checkedout-lane", worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "checkedout-lane")
	if e == nil {
		t.Fatal("checkedout-lane not reported skipped")
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "checked out at") || !strings.Contains(reason, otherPath) {
		t.Errorf("reason = %q, want it to say checked out at %s", reason, otherPath)
	}
	if !branchExistsInRepo(t, r.mainRoot, "checkedout-lane") {
		t.Error("branch was deleted despite being checked out elsewhere")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "checkedout-lane"); scErr != nil {
		t.Errorf("sidecar deleted despite the checked-out skip: %v", scErr)
	}
}

func TestWorktreePrune_Sweep2_UnmergedResidueKept(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "residue-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	commitInWorktree(t, path, "a.txt", "a\n", "advance residue-lane")
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// main is NOT advanced/merged.
	if _, err := r.removeOp(t, map[string]any{"name": "residue-lane"}); err != nil {
		t.Fatalf("remove (branch kept): %v", err)
	}

	canonicalMain := r.canonicalMain(t)
	metaDir := r.metaDir(canonicalMain)
	ageSidecar(t, metaDir, "residue-lane", worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "residue-lane")
	if e == nil {
		t.Fatal("residue-lane not reported skipped")
	}
	if e["reason"] != "unmerged" {
		t.Errorf("reason = %v, want unmerged", e["reason"])
	}
	if !branchExistsInRepo(t, r.mainRoot, "residue-lane") {
		t.Error("unmerged branch residue was deleted")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "residue-lane"); scErr != nil {
		t.Errorf("sidecar deleted despite unmerged residue: %v", scErr)
	}
}

// ============================================================
// prune — sweep 3 (git registry hygiene)
// ============================================================

func TestWorktreePrune_Sweep3_RunsWhenAllPrunableManaged(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "vanished-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// Out-of-band directory deletion (never went through `remove`): git still
	// has the worktree registered and flags it prunable.
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove dir out of band: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if out["registry_pruned"] != true {
		t.Errorf("registry_pruned = %v, want true (only a managed entry is prunable)", out["registry_pruned"])
	}

	after := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	for _, e := range worktree.ParsePorcelain(after) {
		if filepath.Clean(e.Path) == filepath.Clean(path) {
			t.Errorf("vanished-lane still registered after sweep 3 ran: %+v", e)
		}
	}
}

func TestWorktreePrune_Sweep3_SkippedWhenNonManagedPrunable(t *testing.T) {
	r := newWorktreeRepo(t)
	siblingRoot := t.TempDir()
	siblingPath := filepath.Join(siblingRoot, "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling-branch", siblingPath, r.head)
	if err := os.RemoveAll(siblingPath); err != nil {
		t.Fatalf("remove sibling dir: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if out["registry_pruned"] != false {
		t.Errorf("registry_pruned = %v, want false (a non-managed entry is prunable)", out["registry_pruned"])
	}
	skipReason, _ := out["registry_skip_reason"].(string)
	if !strings.Contains(skipReason, siblingPath) {
		t.Errorf("registry_skip_reason = %q, want it to name %s", skipReason, siblingPath)
	}

	after := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	found := false
	for _, e := range worktree.ParsePorcelain(after) {
		if filepath.Clean(e.Path) == filepath.Clean(siblingPath) {
			found = true
		}
	}
	if !found {
		t.Error("non-managed prunable sibling was deregistered; sweep 3 must have skipped")
	}
}

// commitOnBranch checks out branch in a throwaway worktree, commits
// name/content on it, and returns the new tip SHA — used to simulate a user
// building on a branch after its managed worktree was removed (no worktree
// remains locally checked out on it).
func commitOnBranch(t *testing.T, root, branch, name, content, msg string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "adopt-scratch")
	wtGit(t, root, "worktree", "add", tmp, branch)
	tip := commitInWorktree(t, tmp, name, content, msg)
	wtGit(t, root, "worktree", "remove", "--force", tmp)
	return tip
}
