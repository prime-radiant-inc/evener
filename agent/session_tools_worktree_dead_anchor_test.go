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
	r, _, live := anchorGoneRepo(t)

	entries, err := r.s.worktreeList(t.Context())
	if err != nil {
		t.Fatalf("worktreeList with a dead anchor = %v, want no error", err)
	}
	found := false
	for _, e := range entries {
		if filepath.Clean(e.Path) == filepath.Clean(live) {
			found = true
		}
	}
	if !found {
		t.Fatalf("list did not report the live lane %q; entries = %+v", live, entries)
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
	found := false
	for _, e := range entries {
		if filepath.Clean(e.Path) == filepath.Clean(path) {
			found = true
		}
	}
	if !found {
		t.Fatalf("list did not report the created lane %q; entries = %+v", path, entries)
	}
}
