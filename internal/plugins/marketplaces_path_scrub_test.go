package plugins

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// Each test below forces a write or rollback primitive to fail with a real
// *fs.PathError - what atomicWriteFile/os.RemoveAll actually return, naming
// this machine's absolute plugin-store path in its own Error() text - and
// asserts the caller's wire error carries none of it. The path only reaches
// m.Stderr (discarded here), never the returned error (#1700).

// ensureFetched (reached here via Browse's lazy backfill) has no rollback of
// its own: the freshly-cloned directory stays on disk, and the metadata save
// that would have recorded it is the only thing that failed.
func TestEnsureFetchedSaveFailureNamesNoPath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if err := m.saveMarketplaces(Marketplaces{"market-a": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"}}}); err != nil {
		t.Fatal(err)
	}
	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error { return os.MkdirAll(dest, 0o755) }

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
	}

	_, err := m.Browse(ctx, "market-a")
	if err == nil {
		t.Fatal("Browse = nil, want the failed backfill save reported")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "market-a") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// AddMarketplace's save failure, with a clean rollback of the fetched clone,
// is a plain refusal - but the save error itself is atomicWriteFile's own,
// which names the store's path.
func TestAddMarketplaceSaveFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
	}

	_, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src})
	if err == nil {
		t.Fatal("AddMarketplace = nil, want the failed save reported")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if _, statErr := os.Stat(m.marketplaceDir("market-a")); !os.IsNotExist(statErr) {
		t.Fatalf("clone survived a rolled-back Add: %v", statErr)
	}
}

// When AddMarketplace's own rollback of the freshly-fetched clone also
// fails, the store is left changed - and the rollback failure (os.RemoveAll's
// own *fs.PathError) must not reach the caller either.
func TestAddMarketplaceRollbackFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")

	origWrite := marketplaceAtomicWriteFile
	origRemove := marketplaceRemoveAll
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = origWrite
		marketplaceRemoveAll = origRemove
	})
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}
	clonePath := m.marketplaceDir("market-a")
	marketplaceRemoveAll = func(p string) error {
		if p == clonePath {
			return &fs.PathError{Op: "remove", Path: clonePath, Err: errors.New("permission denied")}
		}
		return origRemove(p)
	}

	_, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src})
	if err == nil {
		t.Fatal("AddMarketplace = nil, want the failed save reported")
	}
	if strings.Contains(err.Error(), clonePath) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "market-a") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// RemoveMarketplace's metadata save is attempted before the clone's own
// removal, so a save failure is a plain refusal - but its own error still
// names the store's path.
func TestRemoveMarketplaceSaveFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")
	if _, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
	}

	err := m.RemoveMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RemoveMarketplace = nil, want the failed save reported")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if _, statErr := os.Stat(m.marketplaceDir("market-a")); statErr != nil {
		t.Fatalf("the clone was removed after a failed save: %v", statErr)
	}
}

// Once RemoveMarketplace's save has landed, a failure removing the now
// orphaned clone is litter the caller cannot undo - previously swallowed
// with only a log line, this now reaches the caller as an error, and that
// error must not carry os.RemoveAll's own path.
func TestRemoveMarketplaceCloneRemovalFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")
	if _, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	clonePath := m.marketplaceDir("market-a")
	origRemove := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = origRemove })
	marketplaceRemoveAll = func(p string) error {
		if p == clonePath {
			return &fs.PathError{Op: "remove", Path: clonePath, Err: errors.New("permission denied")}
		}
		return origRemove(p)
	}

	err := m.RemoveMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RemoveMarketplace = nil, want the failed clone removal reported")
	}
	if strings.Contains(err.Error(), clonePath) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	list, _ := m.ListMarketplaces(ctx)
	if _, ok := list["market-a"]; ok {
		t.Fatalf("marketplace still listed after its save-then-remove: %v", list)
	}
}

// RefreshMarketplace's save failure, after a directory-source refresh (which
// touches no disk beyond LastUpdated), is a plain metadata-save failure.
func TestRefreshMarketplaceSaveFailureNamesNoPath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceDirectory, Path: makeMarketplaceRepoDirNoGit(t, "market-a")}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
	}

	err := m.RefreshMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RefreshMarketplace = nil, want the failed save reported")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
}

// EditMarketplace's fail closure returns the save error unchanged when the
// outer rollback (undoSwap/runUndo) succeeds - the common case for a
// re-source's save failing after the swap-in. saveMarketplaces' error is
// atomicWriteFile's own *fs.PathError, naming this machine's absolute
// plugin-store path directly.
func TestEditMarketplaceSaveFailureRollbackSucceedsNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name %s", err, marketplacesFileName)
	}
}

// EditMarketplace's fail closure only scrubs when its own outer rollback
// (undoSwap/runUndo) also fails - a re-source's swap-in succeeding but the
// save after it failing, and the swap's own undo failing too.
func TestEditMarketplaceRollbackFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	swappedIn := m.marketplaceDir("acme")
	origWrite := marketplaceAtomicWriteFile
	origRemove := marketplaceRemoveAll
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = origWrite
		marketplaceRemoveAll = origRemove
	})
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("boom")
	}
	marketplaceRemoveAll = func(p string) error {
		if p == swappedIn {
			return &fs.PathError{Op: "remove", Path: swappedIn, Err: errors.New("permission denied")}
		}
		return origRemove(p)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	if strings.Contains(err.Error(), swappedIn) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}
