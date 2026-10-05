//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestOpenSessionScratchIsPerSessionAndReopensTheSamePath(t *testing.T) {
	base := t.TempDir()
	root, err := OpenSessionScratch(base, t.TempDir(), "ROOT1", "ROOT1")
	if err != nil {
		t.Fatalf("open root scratch: %v", err)
	}
	child, err := OpenSessionScratch(base, t.TempDir(), "ROOT1", "CHILD1")
	if err != nil {
		t.Fatalf("open child scratch: %v", err)
	}
	tree := filepath.Join(base, sessionScratchTreePrefix+"ROOT1")
	if root.Dir != filepath.Join(tree, "ROOT1") || child.Dir != filepath.Join(tree, "CHILD1") {
		t.Fatalf("root %q and child %q are not siblings under %q", root.Dir, child.Dir, tree)
	}
	if err := os.WriteFile(filepath.Join(root.Dir, "kept.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Retain(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenSessionScratch(base, t.TempDir(), "ROOT1", "ROOT1")
	if err != nil {
		t.Fatalf("reopen root scratch: %v", err)
	}
	if again.Dir != root.Dir {
		t.Fatalf("reopened %q, want the same %q", again.Dir, root.Dir)
	}
	if _, err := os.Stat(filepath.Join(again.Dir, "kept.txt")); err != nil {
		t.Fatalf("reopened scratch lost its file: %v", err)
	}
	_ = again.Retain()
	_ = child.Retain()
}

func TestOpenSessionScratchRefusesALiveLease(t *testing.T) {
	base := t.TempDir()
	first, err := OpenSessionScratch(base, t.TempDir(), "ROOT2", "ROOT2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Retain() })
	if _, err := OpenSessionScratch(base, t.TempDir(), "ROOT2", "ROOT2"); err == nil {
		t.Fatal("a second open of a scratch whose lease is held succeeded")
	}
}

func TestOpenSessionScratchRefusesASymlinkedTree(t *testing.T) {
	base := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(base, sessionScratchTreePrefix+"ROOT3")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionScratch(base, t.TempDir(), "ROOT3", "ROOT3"); err == nil {
		t.Fatal("opened a scratch tree that is a symlink")
	}
}

func TestOpenSessionScratchRefusesAnUnsafeID(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "."} {
		if _, err := OpenSessionScratch(t.TempDir(), t.TempDir(), id, "X"); err == nil {
			t.Errorf("root id %q accepted", id)
		}
		if _, err := OpenSessionScratch(t.TempDir(), t.TempDir(), "X", id); err == nil {
			t.Errorf("session id %q accepted", id)
		}
	}
}

func TestSessionScratchPruneCachesKeepsEverythingElse(t *testing.T) {
	s, err := OpenSessionScratch(t.TempDir(), t.TempDir(), "ROOT4", "ROOT4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Retain() })
	readOnlyModuleTree(t, s.Dir) // gomodcache/...
	if err := os.MkdirAll(filepath.Join(s.Dir, "gocache", "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "notes.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneCaches(); err != nil {
		t.Fatalf("PruneCaches: %v", err)
	}
	for _, name := range SessionCacheDirNames {
		if _, err := os.Lstat(filepath.Join(s.Dir, name)); !os.IsNotExist(err) {
			t.Errorf("cache %s survived the prune: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "notes.md")); err != nil {
		t.Errorf("the prune removed a non-cache file: %v", err)
	}
}

func TestRemoveSessionScratchTreeRemovesRootAndChildren(t *testing.T) {
	base := t.TempDir()
	prev := sessionScratchTempDir
	sessionScratchTempDir = func() string { return base }
	t.Cleanup(func() { sessionScratchTempDir = prev })
	root, err := OpenSessionScratch("", t.TempDir(), "ROOT5", "ROOT5")
	if err != nil {
		t.Fatal(err)
	}
	child, err := OpenSessionScratch("", t.TempDir(), "ROOT5", "CHILD5")
	if err != nil {
		t.Fatal(err)
	}
	readOnlyModuleTree(t, child.Dir)
	_ = root.Retain()
	_ = child.Retain()
	if err := RemoveSessionScratchTree("ROOT5"); err != nil {
		t.Fatalf("RemoveSessionScratchTree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, sessionScratchTreePrefix+"ROOT5")); !os.IsNotExist(err) {
		t.Fatalf("the tree survived: %v", err)
	}
	if err := RemoveSessionScratchTree("NEVERMINTED"); err != nil {
		t.Fatalf("removing an absent tree: %v", err)
	}
}

// SessionScratchTreeRootIDs lists the roots whose trees exist, so a reconcile
// visits only those instead of every session ever recorded.
func TestSessionScratchTreeRootIDsListsExistingTrees(t *testing.T) {
	base := t.TempDir()
	prev := sessionScratchTempDir
	sessionScratchTempDir = func() string { return base }
	t.Cleanup(func() { sessionScratchTempDir = prev })
	for _, root := range []string{"ROOTA", "ROOTB"} {
		s, err := OpenSessionScratch("", t.TempDir(), root, root)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Retain()
	}
	if err := os.MkdirAll(filepath.Join(base, "evener-sandbox-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := SessionScratchTreeRootIDs()
	slices.Sort(got)
	if want := []string{"ROOTA", "ROOTB"}; !slices.Equal(got, want) {
		t.Errorf("SessionScratchTreeRootIDs = %v, want %v", got, want)
	}
}

// A daemon started with its own TMPDIR keeps its trees in a temp dir the hub's
// process does not use. Listing and removal also look in the extra bases the
// caller names, the temp dirs the sessions' metas record.
func TestSessionScratchTreesInAnExtraBaseAreFoundAndRemoved(t *testing.T) {
	hubBase, daemonBase := t.TempDir(), t.TempDir()
	prev := sessionScratchTempDir
	sessionScratchTempDir = func() string { return hubBase }
	t.Cleanup(func() { sessionScratchTempDir = prev })
	s, err := OpenSessionScratch(daemonBase, t.TempDir(), "ROOTE", "ROOTE")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Retain()

	if got := SessionScratchTreeRootIDs(daemonBase); !slices.Contains(got, "ROOTE") {
		t.Errorf("SessionScratchTreeRootIDs(%s) = %v, want ROOTE", daemonBase, got)
	}
	if err := RemoveSessionScratchTree("ROOTE", daemonBase); err != nil {
		t.Fatalf("RemoveSessionScratchTree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(daemonBase, sessionScratchTreePrefix+"ROOTE")); !os.IsNotExist(err) {
		t.Errorf("the tree in the extra base survived: %v", err)
	}
}

// A removal that crashed after renaming a scratch to its tombstone leaves the
// tombstone; the next removal takes it and the tree.
func TestRemoveSessionScratchTreeTakesALeftoverTombstone(t *testing.T) {
	base := t.TempDir()
	prev := sessionScratchTempDir
	sessionScratchTempDir = func() string { return base }
	t.Cleanup(func() { sessionScratchTempDir = prev })
	tree := filepath.Join(base, sessionScratchTreePrefix+"ROOT9")
	tombstone := filepath.Join(tree, sessionScratchTombstonePrefix+"ROOT9")
	if err := os.MkdirAll(tombstone, 0o700); err != nil {
		t.Fatal(err)
	}
	readOnlyModuleTree(t, tombstone)
	if err := RemoveSessionScratchTree("ROOT9"); err != nil {
		t.Fatalf("RemoveSessionScratchTree: %v", err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Errorf("the tree with a leftover tombstone survived: %v", err)
	}
}

// A launch that fails disposes the scratch it named; with no other session in
// the tree, the tree goes too rather than lingering empty forever.
func TestCleanupOfTheLastNamedScratchRemovesItsTree(t *testing.T) {
	base := t.TempDir()
	only, err := OpenSessionScratch(base, t.TempDir(), "ROOT7", "ROOT7")
	if err != nil {
		t.Fatal(err)
	}
	if err := only.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, sessionScratchTreePrefix+"ROOT7")); !os.IsNotExist(err) {
		t.Errorf("the empty tree survived its last scratch's removal: %v", err)
	}

	kept, err := OpenSessionScratch(base, t.TempDir(), "ROOT8", "ROOT8")
	if err != nil {
		t.Fatal(err)
	}
	_ = kept.Retain()
	gone, err := OpenSessionScratch(base, t.TempDir(), "ROOT8", "CHILD8")
	if err != nil {
		t.Fatal(err)
	}
	if err := gone.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(kept.Dir); err != nil {
		t.Errorf("removing one scratch took its sibling %s: %v", kept.Dir, err)
	}
}

// A session whose daemon is still running holds its scratch lease. Removing
// the tree (on archive) skips that session's scratch and the tree around it,
// and removes the released sessions' scratch.
func TestRemoveSessionScratchTreeSkipsALiveSession(t *testing.T) {
	base := t.TempDir()
	prev := sessionScratchTempDir
	sessionScratchTempDir = func() string { return base }
	t.Cleanup(func() { sessionScratchTempDir = prev })
	live, err := OpenSessionScratch("", t.TempDir(), "ROOT6", "ROOT6")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Retain() })
	done, err := OpenSessionScratch("", t.TempDir(), "ROOT6", "CHILD6")
	if err != nil {
		t.Fatal(err)
	}
	_ = done.Retain()

	if err := RemoveSessionScratchTree("ROOT6"); err != nil {
		t.Fatalf("RemoveSessionScratchTree: %v", err)
	}
	if _, err := os.Stat(live.Dir); err != nil {
		t.Errorf("removal took the live session's scratch %s: %v", live.Dir, err)
	}
	if _, err := os.Lstat(done.Dir); !os.IsNotExist(err) {
		t.Errorf("removal left the released session's scratch %s: %v", done.Dir, err)
	}

	// Once the live session ends, a second removal takes the rest.
	_ = live.Retain()
	if err := RemoveSessionScratchTree("ROOT6"); err != nil {
		t.Fatalf("RemoveSessionScratchTree after the end: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, sessionScratchTreePrefix+"ROOT6")); !os.IsNotExist(err) {
		t.Errorf("the tree survived its last session's end: %v", err)
	}
}
