package plugins

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
// orphaned clone is litter the caller cannot undo, reported as an error that
// must not carry os.RemoveAll's own path.
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
	if !errors.Is(err, ErrMarketplaceUnregisteredCloneRemains) {
		t.Fatalf("err = %v, want errors.Is(err, ErrMarketplaceUnregisteredCloneRemains): a caller must be able to tell this applied-with-litter outcome from a plain refusal", err)
	}
	list, listErr := m.ListMarketplaces(ctx)
	if listErr != nil {
		t.Fatalf("ListMarketplaces: %v", listErr)
	}
	if _, ok := list["market-a"]; ok {
		t.Fatalf("marketplace still listed after its save-then-remove: %v", list)
	}
}

// A retry after the litter above finds no entry for "market-a" in
// known_marketplaces.json (the unregister save already landed), so it reads
// as a plain lookup miss - ErrMarketplaceNotFound, exactly as a name that was
// never registered at all. RemoveMarketplace does not touch the filesystem
// on a miss to tell the two apart: name is caller-controlled here, and
// deriving m.marketplaceDir(name) from an unvalidated name to stat or remove
// it would let "", "..", or "../x" reach outside the marketplace it was
// never validated against (see
// TestRemoveMarketplace_LookupMissTouchesNothingOnDisk). Whoever wants the
// litter cleaned up retries some other way.
func TestRemoveMarketplaceRetryAfterCloneRemovalFailureReportsNotFound(t *testing.T) {
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

	if err := m.RemoveMarketplace(ctx, "market-a"); !errors.Is(err, ErrMarketplaceUnregisteredCloneRemains) {
		t.Fatalf("first RemoveMarketplace = %v, want ErrMarketplaceUnregisteredCloneRemains", err)
	}
	mustExist(t, clonePath)

	// The retry: the entry is already gone from known_marketplaces.json, so
	// this is a plain lookup miss.
	err := m.RemoveMarketplace(ctx, "market-a")
	if !errors.Is(err, ErrMarketplaceNotFound) {
		t.Fatalf("retry RemoveMarketplace = %v, want ErrMarketplaceNotFound", err)
	}
	mustExist(t, clonePath) // the retry never touches the filesystem on a miss
}

// RemoveMarketplace derives m.marketplaceDir(name) from name without
// validating it first. A miss must never reach the filesystem with that
// unvalidated name: "" resolves to the marketplaces directory itself, and
// ".." to its parent - either one handed to marketplaceRemoveAll on a
// caller-controlled miss would delete far more than a leftover clone.
func TestRemoveMarketplace_LookupMissTouchesNothingOnDisk(t *testing.T) {
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

	var removed []string
	origRemove := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = origRemove })
	marketplaceRemoveAll = func(p string) error {
		removed = append(removed, p)
		return origRemove(p)
	}

	for _, name := range []string{"", "..", "../x", "does-not-exist"} {
		err := m.RemoveMarketplace(ctx, name)
		if !errors.Is(err, ErrMarketplaceNotFound) {
			t.Fatalf("RemoveMarketplace(%q) = %v, want ErrMarketplaceNotFound", name, err)
		}
	}
	if len(removed) != 0 {
		t.Fatalf("marketplaceRemoveAll called on a lookup miss: %v", removed)
	}
	if _, err := os.Stat(m.marketplacesDir()); err != nil {
		t.Fatalf("the marketplaces directory itself was touched: %v", err)
	}
	mustExist(t, m.marketplaceDir("market-a"))
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

// The two tests above both re-source under the same name, so neither reaches
// saveRename (only a rename, renaming=true, calls it). saveRename saves the
// registry first, so a plain rename's registry-save failure must name
// installed_plugins.json, not known_marketplaces.json - the file that
// actually failed.
func TestEditMarketplaceRenameRegistrySaveFailureNamesRegistryFile(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	regPath := m.registryPath()
	origSave := installSaveRegistry
	t.Cleanup(func() { installSaveRegistry = origSave })
	installSaveRegistry = func(string, Registry) error {
		return &fs.PathError{Op: "write", Path: regPath, Err: errors.New("permission denied")}
	}

	_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
	if err == nil {
		t.Fatal("expected the registry save to fail")
	}
	if strings.Contains(err.Error(), regPath) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), registryFileName) {
		t.Fatalf("err = %v, want it to name %s", err, registryFileName)
	}
	if strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name the file that actually failed (%s), not %s", err, registryFileName, marketplacesFileName)
	}
}

// When saveRename's marketplaces save fails and the registry restore that
// follows also fails, the store is left between the two names -
// errStoreBetweenNames marks that state. The scrubbed error saveFailed
// returns for this branch must still carry that identity so a caller can
// errors.Is against it.
func TestEditMarketplaceRenameBetweenNamesErrorSurvivesIdentity(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(p string, b []byte, mode os.FileMode) error {
		if p == path {
			return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
		}
		return origWrite(p, b, mode)
	}

	origSave := installSaveRegistry
	saveCalls := 0
	t.Cleanup(func() { installSaveRegistry = origSave })
	installSaveRegistry = func(p string, reg Registry) error {
		saveCalls++
		if saveCalls == 1 {
			// The rekeyed registry saveRename writes before the marketplaces
			// file - lets that one land so the restore below is a real
			// second write, not a no-op.
			return origSave(p, reg)
		}
		return &fs.PathError{Op: "write", Path: p, Err: errors.New("permission denied")}
	}

	_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
	if err == nil {
		t.Fatal("expected both the marketplaces save and the registry restore to fail")
	}
	if !errors.Is(err, errStoreBetweenNames) {
		t.Fatalf("err = %v, want errors.Is(err, errStoreBetweenNames)", err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name %s", err, marketplacesFileName)
	}
}

// EditMarketplace's fail closure preserves errStoreBetweenNames when the
// outer directory rollback succeeds (the test above); this covers the other
// branch, where the outer rollback (undoSwap here) ALSO fails and fail
// returns storeChangeRollbackFailed instead of the scrubbed cause directly -
// that helper has to re-attach the sentinel itself, or the identity the
// first branch preserves is lost the moment a second thing goes wrong.
func TestEditMarketplaceRenameBetweenNamesRollbackFailureSurvivesIdentity(t *testing.T) {
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
	if _, err := m.Install(ctx, "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	path := m.marketplacesFile()
	swappedIn := m.marketplaceDir("beta")
	origWrite := marketplaceAtomicWriteFile
	origRemove := marketplaceRemoveAll
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = origWrite
		marketplaceRemoveAll = origRemove
	})
	marketplaceAtomicWriteFile = func(p string, b []byte, mode os.FileMode) error {
		if p == path {
			return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
		}
		return origWrite(p, b, mode)
	}
	marketplaceRemoveAll = func(p string) error {
		if p == swappedIn {
			return &fs.PathError{Op: "remove", Path: swappedIn, Err: errors.New("permission denied")}
		}
		return origRemove(p)
	}

	origSave := installSaveRegistry
	saveCalls := 0
	t.Cleanup(func() { installSaveRegistry = origSave })
	installSaveRegistry = func(p string, reg Registry) error {
		saveCalls++
		if saveCalls == 1 {
			// The rekeyed registry saveRename writes before the marketplaces
			// file - lets that one land so the restore below is a real
			// second write, not a no-op.
			return origSave(p, reg)
		}
		return &fs.PathError{Op: "write", Path: p, Err: errors.New("permission denied")}
	}

	_, err := m.EditMarketplace(ctx, "acme", "beta", &Source{Kind: SourceURL, URL: repoB})
	if err == nil {
		t.Fatal("expected the marketplaces save, the registry restore, and the swap rollback to all fail")
	}
	if !errors.Is(err, errStoreBetweenNames) {
		t.Fatalf("err = %v, want errors.Is(err, errStoreBetweenNames)", err)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), swappedIn) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
}

// A ListMarketplaces call that finds a legacy-named marketplace reaches
// saveRename through the migration barrier (migrateMarketplaceName), not
// through EditMarketplace - and its error surfaces to an RPC caller just as
// directly. saveRename's own scrub has to cover this caller too, not only
// EditMarketplace's.
func TestMigrationTriggeredSaveFailureNamesNoPath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")

	path := m.marketplacesFile()
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(p string, data []byte, perm os.FileMode) error {
		if filepath.Base(p) == marketplacesFileName {
			return &fs.PathError{Op: "write", Path: path, Err: errors.New("permission denied")}
		}
		return origWrite(p, data, perm)
	}

	_, err := m.ListMarketplaces(context.Background())
	if err == nil {
		t.Fatal("ListMarketplaces = nil, want the migration's failed save reported")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name %s", err, marketplacesFileName)
	}
}
