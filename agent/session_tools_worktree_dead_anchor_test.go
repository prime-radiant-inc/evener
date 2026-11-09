package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This file is the behavior surface for issue #4671: a session whose recorded
// working directory (its "anchor") has been deleted out from under it. Each
// test asserts behavior — a typed error, or a side effect that did or did not
// happen — never copy.

// anchorGoneRepo creates one live managed lane and then a second lane that the
// session enters, and deletes the second lane's directory. The session is left
// anchored at a path that no longer exists, while its restore environment still
// names the live main root — exactly the state a `git worktree remove` from the
// main checkout leaves a running session in. Returns the repo, the dead lane
// path, and the still-live lane path.
func anchorGoneRepo(t *testing.T) (r *wtRepo, dead, live string) {
	t.Helper()
	r = newWorktreeRepo(t)
	liveRes, err := r.create(t, map[string]any{"name": "live-lane"})
	if err != nil {
		t.Fatalf("create live lane: %v", err)
	}
	live = liveRes["path"].(string)
	deadRes, err := r.create(t, map[string]any{"name": "dead-lane"})
	if err != nil {
		t.Fatalf("create dead lane: %v", err)
	}
	dead = deadRes["path"].(string)
	if got := r.s.currentEnv().WorkingDirectory(); filepath.Clean(got) != filepath.Clean(dead) {
		t.Fatalf("session anchor = %q, want the dead lane %q", got, dead)
	}
	if err := os.RemoveAll(dead); err != nil {
		t.Fatalf("delete dead lane directory: %v", err)
	}
	if _, statErr := os.Stat(dead); !os.IsNotExist(statErr) {
		t.Fatalf("dead lane directory still present after removal: %v", statErr)
	}
	return r, dead, live
}

// T1: `list` must enumerate the lanes that exist without dying on the lstat of
// a path that is gone.
func TestWorktreeDeadAnchor_ListEnumeratesLiveLanes(t *testing.T) {
	t.Parallel()
	r, dead, live := anchorGoneRepo(t)

	entries, err := r.s.worktreeList(t.Context())
	if err != nil {
		t.Fatalf("worktreeList with a dead anchor = %v, want no error", err)
	}
	if _, ok := worktreeEntryByPath(entries, live); !ok {
		t.Fatalf("list did not report the live lane %q; entries = %+v", live, entries)
	}
	// The lane whose directory was removed with a bare delete stays in git's
	// registry, so list reports it rather than dropping it silently, still
	// carrying this session's lock (git does not mark a locked worktree prunable).
	deadEntry, ok := worktreeEntryByPath(entries, dead)
	if !ok {
		t.Fatalf("list dropped the deleted lane %q entirely; entries = %+v", dead, entries)
	}
	if !deadEntry.Locked {
		t.Fatalf("deleted lane %q reported unlocked; entries = %+v", dead, entries)
	}
}

// worktreeEntryByPath finds the listed entry for path (cleaned comparison).
func worktreeEntryByPath(entries []WorktreeListEntry, path string) (WorktreeListEntry, bool) {
	for _, e := range entries {
		if filepath.Clean(e.Path) == filepath.Clean(path) {
			return e, true
		}
	}
	return WorktreeListEntry{}, false
}

// The other mutating operations refuse with the same named error rather than
// leaking the lstat (issue #4671: every operation names the dead path).
func TestWorktreeDeadAnchor_MutatingOpsRefuseNamed(t *testing.T) {
	t.Parallel()
	r, dead, live := anchorGoneRepo(t)

	check := func(op string, err error) {
		t.Helper()
		var gone *worktreeAnchorGoneError
		if !errors.As(err, &gone) {
			t.Fatalf("%s error = %v, want *worktreeAnchorGoneError", op, err)
		}
		if filepath.Clean(gone.path) != filepath.Clean(dead) {
			t.Fatalf("%s named path %q, want the dead lane %q", op, gone.path, dead)
		}
	}
	_, createErr := r.create(t, map[string]any{"name": "another"})
	check("create", createErr)
	_, removeErr := r.s.worktreeRemove(t.Context(), "live-lane", false, false, false)
	check("remove", removeErr)
	_, pruneErr := r.s.worktreePrune(t.Context(), 0)
	check("prune", pruneErr)

	// Each refusal aborts before touching anything: create made no lane, remove
	// did not take the live lane, and the live lane is still on disk.
	if _, statErr := os.Stat(r.managedPath(t, r.canonicalMain(t), "another")); !os.IsNotExist(statErr) {
		t.Fatalf("refused create left a lane behind: %v", statErr)
	}
	if _, statErr := os.Stat(live); statErr != nil {
		t.Fatalf("refused remove took the live lane: %v", statErr)
	}
}

// T2: `switch` and `exit` must refuse with the named dead-anchor error, naming
// the dead path, and must not move the session's environment.
func TestWorktreeDeadAnchor_SwitchAndExitRefuseNamed(t *testing.T) {
	t.Parallel()
	r, dead, _ := anchorGoneRepo(t)

	before := r.s.currentEnv().WorkingDirectory()

	_, swErr := r.switchOp(t, map[string]any{"name": "live-lane"})
	var swGone *worktreeAnchorGoneError
	if !errors.As(swErr, &swGone) {
		t.Fatalf("switch error = %v, want *worktreeAnchorGoneError", swErr)
	}
	if filepath.Clean(swGone.path) != filepath.Clean(dead) {
		t.Fatalf("switch named path %q, want the dead lane %q", swGone.path, dead)
	}
	if got := r.s.currentEnv().WorkingDirectory(); filepath.Clean(got) != filepath.Clean(before) {
		t.Fatalf("switch moved the environment to %q despite a dead anchor", got)
	}

	_, exErr := r.exitOp(t)
	var exGone *worktreeAnchorGoneError
	if !errors.As(exErr, &exGone) {
		t.Fatalf("exit error = %v, want *worktreeAnchorGoneError", exErr)
	}
	if filepath.Clean(exGone.path) != filepath.Clean(dead) {
		t.Fatalf("exit named path %q, want the dead lane %q", exGone.path, dead)
	}
	if r.s.worktreeRestoreEnv == nil {
		t.Fatal("exit cleared the restore env despite refusing")
	}
	if got := r.s.currentEnv().WorkingDirectory(); filepath.Clean(got) != filepath.Clean(before) {
		t.Fatalf("exit moved the environment to %q despite a dead anchor", got)
	}
}

// T4: a worktree-isolated delegate spawn must degrade to the named recovery
// error, not the raw resolve-project lstat.
func TestWorktreeDeadAnchor_IsolatedDelegateSpawnRefusesNamed(t *testing.T) {
	t.Parallel()
	r, dead, _ := anchorGoneRepo(t)

	result := r.s.createDelegate(t.Context(), delegateArgs{Task: "isolated work", Isolation: "worktree"})
	if result.Err == nil {
		t.Fatal("isolated delegate spawn succeeded against a dead anchor")
	}
	var gone *worktreeAnchorGoneError
	if !errors.As(result.Err, &gone) {
		t.Fatalf("isolated spawn error = %v, want *worktreeAnchorGoneError", result.Err)
	}
	if filepath.Clean(gone.path) != filepath.Clean(dead) {
		t.Fatalf("isolated spawn named path %q, want the dead lane %q", gone.path, dead)
	}
}

// Preservation: with a live anchor, listing still works exactly as before.
func TestWorktreeDeadAnchor_LiveAnchorListStillWorks(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	entries, err := r.s.worktreeList(t.Context())
	if err != nil {
		t.Fatalf("worktreeList with a live anchor = %v, want no error", err)
	}
	if _, ok := worktreeEntryByPath(entries, path); !ok {
		t.Fatalf("list did not report the created lane %q; entries = %+v", path, entries)
	}
}
