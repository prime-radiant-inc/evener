//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
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
