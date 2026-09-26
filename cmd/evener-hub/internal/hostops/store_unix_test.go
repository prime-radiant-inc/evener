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
