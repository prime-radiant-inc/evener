//go:build linux || darwin

package hostops

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestTwoHandlesThroughASymlinkedRootShareTheStore pins the store-key rule where
// a state root is reached through a symlink: both spellings name one store file,
// so they must share one store mutex and one state rather than each holding their
// own lock over their own snapshot and overwriting the other's records.
func TestTwoHandlesThroughASymlinkedRootShareTheStore(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	direct, err := Open(StorePath(root))
	if err != nil {
		t.Fatalf("Open(direct): %v", err)
	}
	through, err := Open(StorePath(link))
	if err != nil {
		t.Fatalf("Open(through the link): %v", err)
	}

	createTestRecord(t, direct, "h1")
	if got := len(through.Records()); got != 1 {
		t.Fatalf("the handle opened through the link sees %d records, want 1: the two spellings do not share one store", got)
	}
	createTestRecord(t, through, "h2")
	if got := len(direct.Records()); got != 2 {
		t.Fatalf("the handle opened directly sees %d records, want 2", got)
	}
}

// TestOpenRefusesAStorePathThatIsNotARegularFile pins the kind rule: a fifo at the
// store path (mode 0600, as a store file would be) would block the read forever
// and turn a stray file into a boot that never finishes, and a symlink would be
// replaced by the first write's rename rather than followed, silently moving the
// store.
func TestOpenRefusesAStorePathThatIsNotARegularFile(t *testing.T) {
	t.Run("fifo", func(t *testing.T) {
		path := StorePath(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
		if _, err := Open(path); err == nil {
			t.Fatalf("Open on a fifo succeeded, want an error")
		}
	})
	t.Run("symlink to a store file", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		writeRawStore(t, target, 0o600, validStoreJSON)
		link := StorePath(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if _, err := Open(link); err == nil {
			t.Fatalf("Open on a symlinked store file succeeded, want an error")
		}
	})
}

// TestOpenRefusesADanglingSymlinkAtTheStorePath pins the kind check's ordering:
// a following stat reports "missing" for a dangling link, so a loader that reads
// the missing-file case first treats it as a fresh empty store — and the next
// write's rename replaces the link instead of writing through it.
func TestOpenRefusesADanglingSymlinkAtTheStorePath(t *testing.T) {
	path := StorePath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "never-created.json"), path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatalf("Open on a dangling symlink succeeded, want an error")
	}
}

// TestOpenRevalidatesTheKindOfAnAlreadyHeldStore pins the other half of the kind
// check: a cached store cell is not a licence to skip the file's kind. A symlink
// whose target resolves to a path whose cell is already held would otherwise hand
// out a handle that writes through the link and replaces it on its next rename.
func TestOpenRevalidatesTheKindOfAnAlreadyHeldStore(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real-store.json")
	writeRawStore(t, target, 0o600, validStoreJSON)
	direct, err := Open(target)
	if err != nil {
		t.Fatalf("Open(target): %v", err)
	}
	createTestRecord(t, direct, "h1")

	// The store path becomes a symlink to that same file, so it resolves to the
	// key whose cell is already held.
	link := StorePath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := Open(link); err == nil {
		t.Fatalf("Open on a symlinked store file succeeded through a held store cell, want an error")
	}
}

// TestAWriteRefusesWhenTheStorePathBecameANonRegularFile pins the kind rule on the
// write path: a path that was a regular store file when it was opened can be
// swapped for a link since, and the rename would replace the link rather than
// refuse. The write refuses, leaves the link alone and leaves no temp file behind.
func TestAWriteRefusesWhenTheStorePathBecameANonRegularFile(t *testing.T) {
	path := StorePath(t.TempDir())
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	record := createTestRecord(t, store, "h1")

	// The store file becomes a symlink behind the store's back.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := store.Transition(record.ID, StateRunning, nil); err == nil {
		t.Fatalf("a write over a symlinked store path succeeded, want a refusal")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the refused write replaced the link instead of refusing")
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("the refused write left temp files behind: %v", temps)
	}
}

// TestAStoreBehindASymlinkedDirectoryIsWritable pins the directory half of the link
// rule: a symlink that resolves to a directory is a directory this store can write
// in — the temporary file and the rename both resolve through it and the link
// itself is never replaced — so Open, Create and Transition all work and the file
// lands in the real directory. (The store *file* is the opposite case: there the
// rename would replace the link, so it is refused.)
func TestAStoreBehindASymlinkedDirectoryIsWritable(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real-hostops")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "hostops")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	store, err := Open(StorePath(root))
	if err != nil {
		t.Fatalf("Open through a symlinked directory: %v", err)
	}
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateRunning, nil); err != nil {
		t.Fatalf("Transition through a symlinked directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "operations.json")); err != nil {
		t.Fatalf("the store file did not land in the real directory: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "hostops"))
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the write replaced the directory link")
	}
}

// TestOpenRefusesAStorePathThroughADanglingLink pins the identity rule for a link
// whose target does not exist yet: the file it names would be reachable two ways
// (the link's spelling and the target's), and two spellings would hold two store
// mutexes and overwrite each other's records, so the link path is refused rather
// than keyed as a spelling of its own.
func TestOpenRefusesAStorePathThroughADanglingLink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	throughLink := filepath.Join(root, "link", "hostops", "operations.json")
	if _, err := Open(throughLink); err == nil {
		t.Fatalf("Open through a link whose target does not exist succeeded, want an error")
	}

	// The spelling that resolves — the target itself — still works, and no alias
	// of it can be opened.
	direct, err := Open(filepath.Join(root, "missing-target", "hostops", "operations.json"))
	if err != nil {
		t.Fatalf("Open on the direct spelling: %v", err)
	}
	createTestRecord(t, direct, "h1")
}
