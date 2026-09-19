//go:build unix

package plugins

import (
	"context"
	"os"
	"syscall"
	"testing"
)

// A legacy name can derive a clone path the filesystem refuses for its length.
// pathPresentNoFollow counts that as absent (pathCannotExist), so the sweep must
// not then call RemoveAll on it — that would fail ENAMETOOLONG and abort a
// directory rename the store has to be able to perform.
func TestEditMarketplace_DirectoryRenameSkipsAnAbsentCloneItCannotStat(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: dir},
		InstallLocation: dir,
	}}); err != nil {
		t.Fatal(err)
	}
	// The over-long condition is injected, so the test does not depend on the
	// host's PATH_MAX: Lstat reports the path cannot exist, and RemoveAll would
	// only fail on it.
	origLstat, origRemoveAll := marketplaceLstat, marketplaceRemoveAll
	t.Cleanup(func() { marketplaceLstat, marketplaceRemoveAll = origLstat, origRemoveAll })
	marketplaceLstat = func(path string) (os.FileInfo, error) {
		if path == clone {
			return nil, &os.PathError{Op: "lstat", Path: path, Err: syscall.ENAMETOOLONG}
		}
		return origLstat(path)
	}
	marketplaceRemoveAll = func(path string) error {
		if path == clone {
			return &os.PathError{Op: "removeall", Path: path, Err: syscall.ENAMETOOLONG}
		}
		return origRemoveAll(path)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("a directory rename failed on an over-long absent clone path: %v", err)
	}
}
