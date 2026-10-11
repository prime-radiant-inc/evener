//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// readOnlyModuleTree builds what `go mod download` leaves in GOMODCACHE: a
// 0555 directory holding a 0444 file, which a plain os.RemoveAll cannot unlink.
func readOnlyModuleTree(t *testing.T, root string) {
	t.Helper()
	mod := filepath.Join(root, "gomodcache", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mod, 0o755) })
}

func TestRemoveTreeRemovesAReadOnlyModuleCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch")
	readOnlyModuleTree(t, dir)
	if err := removeTree(dir); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after removeTree: %v", err)
	}
}

func TestRemoveTreeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o755) })
	dir := filepath.Join(t.TempDir(), "scratch")
	readOnlyModuleTree(t, dir)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(dir); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("symlink target removed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Errorf("symlink target mode = %04o, want 0555 untouched", got)
	}
}

func TestSessionScratchCleanupRemovesAReadOnlyModuleCache(t *testing.T) {
	scratch, err := NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	readOnlyModuleTree(t, scratch.Dir)
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Lstat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after Cleanup: %v", err)
	}
}
