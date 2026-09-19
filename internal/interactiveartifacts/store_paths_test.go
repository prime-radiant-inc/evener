package interactiveartifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreRejectsReplaceableTraversalBeforeDatabaseCreation(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "alias"}[alias], func(t *testing.T) {
			base := t.TempDir()
			unsafe := filepath.Join(base, "writable")
			requireNoError(t, os.Mkdir(unsafe, 0700))
			requireNoError(t, os.Chmod(unsafe, 0777))
			root := filepath.Join(unsafe, "private")
			requireNoError(t, os.Mkdir(root, 0700))
			if alias {
				target := filepath.Join(base, "target")
				requireNoError(t, os.Mkdir(target, 0700))
				requireNoError(t, os.Symlink(target, filepath.Join(unsafe, "alias")))
				root = filepath.Join(unsafe, "alias", "private")
			}
			path := filepath.Join(root, "store.sqlite")
			s, err := OpenStore(path, StoreOptions{})
			if err == nil {
				requireNoError(t, s.Close())
				t.Fatal("opened through replaceable ancestor")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("SQLite file created before rejecting traversal: %v", err)
			}
		})
	}
}

func TestStoreRejectsUnsafeSymlinkTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "writable")
	requireNoError(t, os.Mkdir(target, 0700))
	requireNoError(t, os.Chmod(target, 0777))
	alias := filepath.Join(base, "alias")
	requireNoError(t, os.Symlink(target, alias))
	s, err := OpenStore(filepath.Join(alias, "private", "store.sqlite"), StoreOptions{})
	if err == nil {
		requireNoError(t, s.Close())
		t.Fatal("opened through unsafe symlink target")
	}
}

func TestStoreBackupRejectsReplaceableTraversal(t *testing.T) {
	s, _, _ := setupStore(t, StoreOptions{})
	root := filepath.Join(t.TempDir(), "writable")
	requireNoError(t, os.Mkdir(root, 0700))
	requireNoError(t, os.Chmod(root, 0777))
	path := filepath.Join(root, "private", "snapshot.sqlite")
	if err := s.Backup(context.Background(), path); err == nil {
		t.Fatal("backup traversed replaceable ancestor")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup file created: %v", err)
	}
}

func TestStoreSafeDirectoryTraversal(t *testing.T) {
	for _, mode := range []os.FileMode{0755, os.ModeSticky | 0777} {
		t.Run(mode.String(), func(t *testing.T) {
			ancestor := filepath.Join(t.TempDir(), "ancestor")
			requireNoError(t, os.Mkdir(ancestor, 0700))
			requireNoError(t, os.Chmod(ancestor, mode))
			openTestStore(t, filepath.Join(ancestor, "private", "store.sqlite"), StoreOptions{})
		})
	}
}

func TestStoreMacOSSystemAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system /var alias")
	}
	dir, err := os.MkdirTemp("/var/tmp", "artifact-path-")
	requireNoError(t, err)
	// This test owns this newly created, empty directory and known database files.
	t.Cleanup(func() {
		for _, name := range []string{"store.sqlite", "store.sqlite-wal", "store.sqlite-shm"} {
			_ = os.Remove(filepath.Join(dir, name))
		}
		requireNoError(t, os.Remove(dir))
	})
	canonical, err := PrepareStoreDirectory(dir)
	requireNoError(t, err)
	want, err := filepath.EvalSymlinks(dir)
	requireNoError(t, err)
	if canonical != want {
		t.Fatalf("canonical root %q want %q", canonical, want)
	}
	openTestStore(t, filepath.Join(dir, "store.sqlite"), StoreOptions{})
}

func TestStorePrivateLeafRejectsAlias(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	requireNoError(t, os.Mkdir(root, 0700))
	alias := filepath.Join(t.TempDir(), "alias")
	requireNoError(t, os.Symlink(root, alias))
	if _, err := PrepareStoreDirectory(alias); err == nil {
		t.Fatal("private leaf accepted alias")
	}
}

func TestStoreRejectsUntrustedStickyOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "untrusted")
	requireNoError(t, os.Mkdir(root, 0700))
	requireNoError(t, os.Chmod(root, os.ModeSticky|0777))
	if err := os.Chown(root, os.Geteuid()+1, -1); errors.Is(err, os.ErrPermission) {
		t.Skip("changing fixture ownership requires privilege; no other-UID filesystem probe performed")
	} else {
		requireNoError(t, err)
	}
	defer func() { requireNoError(t, os.Chown(root, os.Geteuid(), -1)) }()
	if _, err := PrepareStoreDirectory(filepath.Join(root, "private")); err == nil {
		t.Fatal("sticky mode trusted a different UID owner")
	}
}

func TestStoreChecksSymlinkTargetBeforeDotDot(t *testing.T) {
	base := t.TempDir()
	writable := filepath.Join(base, "writable")
	requireNoError(t, os.Mkdir(writable, 0700))
	requireNoError(t, os.Chmod(writable, 0777))
	requireNoError(t, os.Mkdir(filepath.Join(base, "target"), 0700))
	alias := filepath.Join(base, "alias")
	requireNoError(t, os.Symlink("writable/../target", alias))
	if _, err := PrepareStoreDirectory(filepath.Join(alias, "private")); err == nil {
		t.Fatal("normalization hid an unsafe symlink target traversal")
	}
}
