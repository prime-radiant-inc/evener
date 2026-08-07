package agent

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for the unmanaged-visibility half of
// `list` and for the `adopt` operation (spec §5 list; §6 "Metadata sidecar":
// a worktree without a sidecar inside the managed dir is provenance-unknown
// and is never auto-adopted). Their subject IS git's observable behavior — a
// hand-made `git worktree add` under the managed root, and what serf may then
// do with it — so they run on the real-git harness defined in
// session_tools_worktree_create_test.go / _switch_test.go.

// adoptOp drives the adopt operation through the registered tool surface.
func (r *wtRepo) adoptOp(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	full := map[string]any{"operation": "adopt"}
	maps.Copy(full, args)
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), full)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("adopt result is %T, want map[string]any", out)
	}
	return m, nil
}

// addUnmanagedWorktreeFixture places a worktree under the managed root by
// hand — a raw `git worktree add`, no sidecar — which is exactly the stray
// serf must surface but never adopt on its own.
func (r *wtRepo) addUnmanagedWorktreeFixture(t *testing.T, name, branch string) string {
	t.Helper()
	path := r.managedPath(t, r.canonicalMain(t), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir managed parent: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "add", "-b", branch, path, r.head)
	return path
}

// listUnmanaged extracts the "unmanaged" slice from a list result.
func listUnmanaged(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, ok := out["unmanaged"].([]map[string]any)
	if !ok {
		t.Fatalf("unmanaged is %T, want []map[string]any", out["unmanaged"])
	}
	return raw
}

// TestWorktreeList_SurfacesUnmanagedWorktreesInTheirOwnSection covers brief
// step 1(a): a raw `git worktree add` under the managed root is reported in a
// labeled, id-less `unmanaged` section — path and branch only — and never
// mixed into the managed entries, whose names are the ones remove/switch
// accept.
func TestWorktreeList_SurfacesUnmanagedWorktreesInTheirOwnSection(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	r.addManagedWorktreeFixture(t, "managed-lane")
	strayPath := r.addUnmanagedWorktreeFixture(t, "stray-lane", "stray-lane")

	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	entries := listEntries(t, out)
	if len(entries) != 1 {
		t.Fatalf("managed entries = %d, want 1: %+v", len(entries), entries)
	}
	findEntry(t, entries, "managed-lane")

	unmanaged := listUnmanaged(t, out)
	if len(unmanaged) != 1 {
		t.Fatalf("unmanaged entries = %d, want 1: %+v", len(unmanaged), unmanaged)
	}
	u := unmanaged[0]
	if u["path"] != strayPath {
		t.Errorf("unmanaged path = %v, want %s", u["path"], strayPath)
	}
	if u["branch"] != "stray-lane" {
		t.Errorf("unmanaged branch = %v, want stray-lane", u["branch"])
	}
	if _, ok := u["name"]; ok {
		t.Errorf("unmanaged entry carries a name (%v); it is id-less until adopted", u["name"])
	}

	msg, _ := out["message"].(string)
	if !strings.Contains(msg, "unmanaged") || !strings.Contains(msg, strayPath) {
		t.Errorf("message = %q, want it to label the unmanaged worktree and name its path", msg)
	}
	// The digest's hands-off guarantee is model-facing text a caller acts on, so
	// it is pinned to the two things remove really delivers: prune never touches
	// an unmanaged worktree, and remove refuses one only without force (step 5
	// gates on !force && !hasSidecar). Pinning the claims, not the sentence,
	// leaves ordinary rewording free but fails if the promise itself changes.
	if !strings.Contains(msg, "never prunes") {
		t.Errorf("message = %q, want the never-prunes guarantee", msg)
	}
	if !strings.Contains(msg, "never removes without force") {
		t.Errorf("message = %q, want the removal guarantee stated as force-gated, not absolute", msg)
	}
}

// TestWorktreeAdopt_RoundTripMakesItManaged covers brief step 1(b): adopt by
// path writes the sidecar recording the ADOPTING session as creator, after
// which the worktree appears managed and switch/remove work on it by name
// with no force.
func TestWorktreeAdopt_RoundTripMakesItManaged(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	strayPath := r.addUnmanagedWorktreeFixture(t, "stray-lane", "stray-lane")

	res, err := r.adoptOp(t, map[string]any{"path": strayPath})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if res["path"] != strayPath {
		t.Errorf("adopt path = %v, want %s", res["path"], strayPath)
	}
	if res["name"] != "stray-lane" || res["branch"] != "stray-lane" {
		t.Errorf("adopt name/branch = %v/%v, want stray-lane/stray-lane", res["name"], res["branch"])
	}

	metaDir := r.metaDir(t, r.canonicalMain(t))
	sc, err := worktree.ReadSidecar(metaDir, "stray-lane")
	if err != nil {
		t.Fatalf("read adopted sidecar: %v", err)
	}
	if sc.CreatorSession != r.s.id {
		t.Errorf("sidecar creator_session = %q, want the adopting session %q", sc.CreatorSession, r.s.id)
	}
	if sc.Name != "stray-lane" || sc.Branch != "stray-lane" {
		t.Errorf("sidecar name/branch = %q/%q, want stray-lane/stray-lane", sc.Name, sc.Branch)
	}
	if sc.OriginalRoot != r.canonicalMain(t) {
		t.Errorf("sidecar original_root = %q, want %q", sc.OriginalRoot, r.canonicalMain(t))
	}
	if sc.CreatedAt == "" {
		t.Error("sidecar created_at is empty")
	}

	// It is managed now: out of the unmanaged section, into the entries.
	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list after adopt: %v", err)
	}
	if u := listUnmanaged(t, out); len(u) != 0 {
		t.Errorf("unmanaged after adopt = %+v, want empty", u)
	}
	e := findEntry(t, listEntries(t, out), "stray-lane")
	if e["has_metadata"] != true {
		t.Errorf("adopted entry has_metadata = %v, want true", e["has_metadata"])
	}

	// switch and remove now work on it by name, with no force.
	if _, err := r.switchOp(t, map[string]any{"name": "stray-lane"}); err != nil {
		t.Fatalf("switch to adopted worktree: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit adopted worktree: %v", err)
	}
	if _, err := r.removeOp(t, map[string]any{"name": "stray-lane"}); err != nil {
		t.Fatalf("remove adopted worktree without force: %v", err)
	}
	if _, statErr := os.Stat(strayPath); !os.IsNotExist(statErr) {
		t.Errorf("adopted worktree still present after remove: %v", statErr)
	}
}

// TestWorktreeAdopt_RefusesOutsideTheManagedRoot covers brief step 1(c): a
// registered worktree that lives outside the managed root is refused. Spec §4
// by-path step 4 keeps those switchable and exitable but never serf's to
// remove or prune, and adoption is the only thing that would change that.
func TestWorktreeAdopt_RefusesOutsideTheManagedRoot(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	sibling := r.addSiblingWorktree(t, "sibling", "sibling-branch")

	_, err := r.adoptOp(t, map[string]any{"path": sibling})
	if err == nil {
		t.Fatal("adopt accepted a worktree outside the managed root")
	}
	if !strings.Contains(err.Error(), "outside the managed worktree root") {
		t.Fatalf("adopt outside managed root: err = %v, want the outside-the-managed-root refusal", err)
	}

	metaDir := r.metaDir(t, r.canonicalMain(t))
	if _, scErr := worktree.ReadSidecar(metaDir, "sibling"); scErr == nil {
		t.Error("refused adopt still wrote a sidecar")
	}
}

// TestWorktreeAdopt_NothingAutoAdopts covers brief step 1(d): an un-adopted
// worktree under the managed root stays un-adopted. remove refuses it on
// unmanaged provenance, prune leaves it alone as sidecar-less, and neither
// writes a sidecar behind the user's back — adoption is explicit only.
func TestWorktreeAdopt_NothingAutoAdopts(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	strayPath := r.addUnmanagedWorktreeFixture(t, "stray-lane", "stray-lane")
	metaDir := r.metaDir(t, r.canonicalMain(t))

	if _, err := r.listOp(t); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "stray-lane"); scErr == nil {
		t.Fatal("list auto-adopted the unmanaged worktree")
	}

	_, err := r.removeOp(t, map[string]any{"name": "stray-lane"})
	if err == nil {
		t.Fatal("remove accepted an un-adopted worktree without force")
	}
	if !strings.Contains(err.Error(), "unmanaged provenance") {
		t.Fatalf("remove of un-adopted worktree: err = %v, want the unmanaged-provenance refusal", err)
	}

	pruneOut, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, pruneOut, "removed"), "stray-lane"); e != nil {
		t.Fatalf("prune removed the un-adopted worktree: %+v", e)
	}
	skipped := findPruneEntry(t, pruneEntries(t, pruneOut, "skipped"), "stray-lane")
	if skipped == nil {
		t.Fatal("prune did not report the un-adopted worktree as skipped")
	}
	if skipped["reason"] != "sidecar-less" {
		t.Errorf("prune skip reason = %v, want sidecar-less", skipped["reason"])
	}

	if _, statErr := os.Stat(strayPath); statErr != nil {
		t.Errorf("un-adopted worktree was destroyed: %v", statErr)
	}
	if _, scErr := worktree.ReadSidecar(metaDir, "stray-lane"); scErr == nil {
		t.Error("remove/prune auto-adopted the unmanaged worktree")
	}
}

// TestWorktreeAdopt_RefusesForeignLockedWorktree pins adopt onto the record's
// existing lock rule set (spec §5 lock state machine): a worktree locked by
// someone else is another owner's occupancy, and claiming its provenance is
// refused rather than raced.
func TestWorktreeAdopt_RefusesForeignLockedWorktree(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	strayPath := r.addUnmanagedWorktreeFixture(t, "stray-lane", "stray-lane")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "serf:some-other-session", strayPath)

	_, err := r.adoptOp(t, map[string]any{"path": strayPath})
	if err == nil {
		t.Fatal("adopt accepted a foreign-locked worktree")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("adopt of foreign-locked worktree: err = %v, want the lock refusal", err)
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), "stray-lane"); scErr == nil {
		t.Error("refused adopt still wrote a sidecar")
	}
}

// TestWorktreeAdopt_RefusesBranchNameMismatch pins the invariant every managed
// operation relies on: a managed worktree's directory name IS its branch name
// (remove step 9 and prune's collector both delete `refs/heads/<name>`).
// Adopting a directory whose branch differs would arm those paths against the
// wrong branch, so it is refused instead.
func TestWorktreeAdopt_RefusesBranchNameMismatch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	strayPath := r.addUnmanagedWorktreeFixture(t, "stray-lane", "some-other-branch")

	_, err := r.adoptOp(t, map[string]any{"path": strayPath})
	if err == nil {
		t.Fatal("adopt accepted a worktree whose branch differs from its directory name")
	}
	if !strings.Contains(err.Error(), "some-other-branch") {
		t.Fatalf("adopt with mismatched branch: err = %v, want the branch/name mismatch refusal", err)
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), "stray-lane"); scErr == nil {
		t.Error("refused adopt still wrote a sidecar")
	}
}

// TestWorktreeAdopt_RefusesAlreadyManagedWorktree keeps adopt from rewriting
// provenance serf already holds: a second adopt must not relabel an existing
// lane's creator.
func TestWorktreeAdopt_RefusesAlreadyManagedWorktree(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	path, _ := res["path"].(string)

	if _, err := r.adoptOp(t, map[string]any{"path": path}); err == nil {
		t.Fatal("adopt accepted an already-managed worktree")
	} else if !strings.Contains(err.Error(), "already managed") {
		t.Fatalf("adopt of managed worktree: err = %v, want the already-managed refusal", err)
	}
}
