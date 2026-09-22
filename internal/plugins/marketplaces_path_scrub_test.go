package plugins

import (
	"context"
	"errors"
	"fmt"
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
		return nil
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

// EditMarketplace's fail closure scrubs every failure that is not already
// path-free. This covers the branch where the closure's own outer rollback
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

// An edit reads both store files before it decides anything. A read failure
// is built as "reading <absolute path>: ...", so it must be scrubbed like the
// write side is (#1854).
func TestEditMarketplaceUnreadableMarketplacesFileNamesNoPath(t *testing.T) {
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
	if err := os.WriteFile(m.marketplacesFile(), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err == nil {
		t.Fatal("expected the marketplaces read to fail")
	}
	if strings.Contains(err.Error(), m.marketplacesFile()) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name %s", err, marketplacesFileName)
	}
}

func TestEditMarketplaceUnreadableRegistryFileNamesNoPath(t *testing.T) {
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
	if err := os.WriteFile(m.registryPath(), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err == nil {
		t.Fatal("expected the registry read to fail")
	}
	if strings.Contains(err.Error(), m.registryPath()) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), registryFileName) {
		t.Fatalf("err = %v, want it to name %s", err, registryFileName)
	}
}

// Scrubbing must not cost a caller the identity it acts on: a cancelled
// request still has to read as context.Canceled even though the lock error it
// came from named the lock file's absolute path (#1854).
func TestEditMarketplaceCancellationKeepsIdentity(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"})
	if err == nil {
		t.Fatal("expected the cancelled edit to fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
}

// A store that cannot be used at all is not a failure to hide behind "see the
// hub's log": the client has to be told to fix the store root, not to retry,
// and that reason carries no path (#1854).
func TestEditMarketplaceStoreRootFailureKeepsItsReason(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	m.Root = ""

	_, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"})
	if err == nil {
		t.Fatal("expected the missing store root to fail the edit")
	}
	if !errors.Is(err, errStoreRootUnset) {
		t.Fatalf("err = %v, want errors.Is(err, errStoreRootUnset)", err)
	}
	if !strings.Contains(err.Error(), "no plugin store root is configured") {
		t.Fatalf("err = %v, want the reason named for the client", err)
	}
}

// Lock contention is transient and actionable - retry shortly - and only the
// lock file's path needs scrubbing, so the reason survives path-free.
func TestEditFailureIdentityPreservesLockContention(t *testing.T) {
	err := fmt.Errorf("wrapped: %w (locked: /some/absolute/path)", errLockContention)
	got := editFailureIdentity(err)
	if !errors.Is(got, errLockContention) {
		t.Fatalf("editFailureIdentity = %v, want errors.Is(..., errLockContention)", got)
	}
	if strings.Contains(got.Error(), "/some/absolute/path") {
		t.Fatalf("editFailureIdentity = %v, want no path", got)
	}
}

// The lock EditMarketplace takes runs the name migration, so a save failure
// there arrives already scrubbed and naming the store file that failed.
// Scrubbing it again would tell the client less than the identical failure
// reports through ListMarketplaces (#1854).
func TestEditMarketplaceMigrationSaveFailureNamesTheStoreFile(t *testing.T) {
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

	_, err := m.EditMarketplace(context.Background(), "a/b", "", nil)
	marketplaceAtomicWriteFile = origWrite
	if err == nil {
		t.Fatal("expected the migration's save to fail the edit")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("err = %v, want it to name %s", err, marketplacesFileName)
	}
}

// A path-free error joined with a path-bearing one is not itself path-free:
// the migration joins saveFailed's error with its rollback and marker
// failures, and passing that composite through would leak the siblings' paths.
func TestEditFailedScrubsACompositeHoldingAPathFreeCause(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	joined := errors.Join(
		&pathFreeError{fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail", "a/b", marketplacesFileName)},
		fmt.Errorf("restoring marketplace clone %s: boom", m.marketplaceDir("beta")),
	)

	got := m.editFailed("a/b", joined)
	if strings.Contains(got.Error(), m.marketplaceDir("beta")) {
		t.Fatalf("editFailed = %v, want no absolute path in the client-facing error", got)
	}
}

// The decision must not depend on the store root's spelling: a root kept with
// a trailing "." still derives normalized paths, so a text comparison against
// the literal root would wave the composite through (#1854).
func TestEditFailedScrubsACompositeUnderANonCanonicalRoot(t *testing.T) {
	base := t.TempDir()
	m := NewManager(base + "/.")
	m.Stderr = io.Discard
	path := filepath.Join(base, "marketplaces", "beta")
	joined := errors.Join(
		&pathFreeError{fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail", "a/b", marketplacesFileName)},
		fmt.Errorf("restoring marketplace clone %s: boom", path),
	)

	got := m.editFailed("a/b", joined)
	if strings.Contains(got.Error(), path) {
		t.Fatalf("editFailed = %v, want no absolute path in the client-facing error", got)
	}
}

// A source kind this build does not accept is the caller's invalid input, not
// a store failure: its message carries no path, so it must reach the caller
// naming the kind and keep its sentinel for the wire classification (#1854).
func TestEditMarketplaceUnsupportedSourceKindKeepsItsReason(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: "bogus", URL: "https://example.invalid/repo.git"})
	if err == nil {
		t.Fatal("expected the unknown source kind to fail the edit")
	}
	if !errors.Is(err, ErrMarketplaceSourceUnsupported) {
		t.Fatalf("err = %v, want errors.Is(err, ErrMarketplaceSourceUnsupported)", err)
	}
	if !strings.Contains(err.Error(), `unsupported marketplace source "bogus"`) {
		t.Fatalf("err = %v, want it to name the source kind", err)
	}
}

// A lock holder's migration reads its marker and its record before it can name
// a marketplace, so those failures reach the wire without ever passing
// migrationFailed. They must be scrubbed at the lock boundary too (#1854).
func TestLockHolderMigrationReadFailureNamesNoPath(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"rename marker", renameMarkerFileName},
		{"migration record", migrationRecordFileName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyMarketplace(t, m, "a/b", "widget")
			path, err := m.storePath(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			origRead := marketplaceReadFile
			t.Cleanup(func() { marketplaceReadFile = origRead })
			marketplaceReadFile = func(p string) ([]byte, error) {
				if p == path {
					return nil, &fs.PathError{Op: "read", Path: p, Err: errors.New("permission denied")}
				}
				return origRead(p)
			}

			_, err = m.ListMarketplaces(context.Background())
			marketplaceReadFile = origRead
			if err == nil {
				t.Fatal("expected the migration read to fail")
			}
			if strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
			}
		})
	}
}

// The wire error drops the rejected root's text, but the local diagnostics a
// doctor run uses still have to show which root was refused (#1854).
func TestStoreRootErrorNamesTheRejectedRoot(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Root = "relative/store"

	err := m.storeRootError()
	if !errors.Is(err, errStoreRootNotAbsolute) {
		t.Fatalf("storeRootError = %v, want errors.Is(..., errStoreRootNotAbsolute)", err)
	}
	if !strings.Contains(err.Error(), "relative/store") {
		t.Fatalf("storeRootError = %v, want it to name the rejected root", err)
	}
}

// A re-source fetches into a staging directory under the store before any
// rollback machinery exists, so its failure names that absolute path too. It
// must be scrubbed like every later step's (#1854).
func TestEditMarketplaceFetchFailureNamesNoPath(t *testing.T) {
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

	staging := m.marketplaceDir(stagingCloneName)
	orig := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = orig })
	marketplaceGitClone = func(_ context.Context, _, _, _, _ string) error {
		return &fs.PathError{Op: "clone", Path: staging, Err: errors.New("permission denied")}
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	marketplaceGitClone = orig
	if err == nil {
		t.Fatal("expected the fetch to fail")
	}
	if strings.Contains(err.Error(), staging) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// The swap's first rename moves an existing install location aside. When that
// rename itself is refused, nothing has been displaced yet, so the store is
// unchanged - but the failure is still os.Rename's own, naming the destination
// directly (#1854).
func TestEditMarketplaceAsideMoveFailureNamesNoPath(t *testing.T) {
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

	dest := m.marketplaceDir("acme")
	aside := m.marketplaceDir(asideCloneName)
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if from == dest && to == aside {
			return &fs.PathError{Op: "rename", Path: dest, Err: errors.New("permission denied")}
		}
		return orig(from, to)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the aside move to fail")
	}
	if strings.Contains(err.Error(), dest) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// A rename's forward step can be refused on its own: the clone moves, the
// cache rename is refused, and the clone move's own undo puts it back. The
// store is unchanged, but the cache rename's *fs.PathError names the cache
// path, so the failure must still be scrubbed (#1854).
func TestEditMarketplaceCacheRenameFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}

	cache := filepath.Join(m.cacheDir(), name)
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if from == cache {
			return &fs.PathError{Op: "rename", Path: cache, Err: errors.New("permission denied")}
		}
		return orig(from, to)
	}

	_, err := m.EditMarketplace(ctx, name, "beta", nil)
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the cache rename to fail")
	}
	if strings.Contains(err.Error(), cache) || strings.Contains(err.Error(), m.marketplaceDir("beta")) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// A directory-source marketplace keeps no clone under the store's name, so a
// re-source to git installs the fresh clone without moving anything aside -
// the swap's install rename is the first primitive it runs and there is no
// rollback to carry. The rename's own *fs.PathError names the destination
// directly, so the failure must be scrubbed even though nothing was left
// changed (#1854).
func TestEditMarketplaceInstallFailureNamesNoPath(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	repo := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	dest := m.marketplaceDir("acme")
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if to == dest {
			return &fs.PathError{Op: "rename", Path: dest, Err: errors.New("permission denied")}
		}
		return orig(from, to)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repo})
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if strings.Contains(err.Error(), dest) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// A re-source's swap renames the old clone aside before it installs the new
// one, so restarting the install and restoring the old clone are both
// renames. When the restore fails too the store is left without an install
// location, and the rename failures' own *fs.PathError text names this
// machine's absolute plugin-store paths. The caller must get the marketplace
// name instead; the paths are the hub's log's, and
// errRenameRollbackIncomplete still marks the state (#1854).
func TestEditMarketplaceSwapRollbackFailureNamesNoPath(t *testing.T) {
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

	dest := m.marketplaceDir("acme")
	aside := m.marketplaceDir(asideCloneName)
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		switch {
		case from == aside:
			// Restoring the old clone the swap set aside fails.
			return &fs.PathError{Op: "rename", Path: aside, Err: errors.New("permission denied")}
		case to == dest:
			// Installing the freshly-fetched clone fails after the aside move.
			return &fs.PathError{Op: "rename", Path: dest, Err: errors.New("permission denied")}
		}
		return orig(from, to)
	}

	_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the re-source's swap to fail")
	}
	if !errors.Is(err, errRenameRollbackIncomplete) {
		t.Fatalf("err = %v, want errors.Is(err, errRenameRollbackIncomplete)", err)
	}
	if strings.Contains(err.Error(), dest) || strings.Contains(err.Error(), aside) {
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
