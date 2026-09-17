package plugins

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every manager write that can leave the store changed has to say so, or the
// hub answers it with no broadcast and every client keeps a listing the store
// no longer matches (#1543/#1572, and ErrStoreChanged's own doc).

// An edit renames directories before it writes the store files. When the write
// fails AND the undo that would move them back fails too, the store is left
// changed — the directories are under neither name they should be.
func TestEditWhoseUndoFailedReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	// A clone-backed marketplace: its install location is a directory inside
	// the store, which is what a rename actually moves. A directory-source
	// marketplace points outside the store and moves nothing, so it cannot
	// reach this state at all (measured).
	plantLegacyMarketplace(t, m, "market-a", "widget")

	// The store write fails, so the edit rolls back; the rollback's own rename
	// of the clone back under its old name then fails too, which is the state
	// this test is about.
	originalWrite := marketplaceAtomicWriteFile
	originalRename := marketplaceRename
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = originalWrite
		marketplaceRename = originalRename
	})
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}
	marketplaceRename = func(from, to string) error {
		if to == m.marketplaceDir("market-a") {
			return errors.New("the rollback rename failed")
		}
		return originalRename(from, to)
	}

	_, err := m.EditMarketplace(context.Background(), "market-a", "market-b", nil)
	marketplaceAtomicWriteFile = originalWrite
	marketplaceRename = originalRename
	if err == nil {
		t.Fatal("EditMarketplace = nil, want the failed write reported")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed so the hub still broadcasts", err)
	}
}

// A directory-source refresh touches nothing on disk: the fetch/pull/reclone
// block only runs for a fetched source, so a directory source's refresh only
// ever changes ref.LastUpdated in memory. A failing record is then a plain
// refusal, not a store change - broadcasting it would have every other client
// refetch a listing that in fact never moved.
func TestRefreshDirectorySourceWhoseRecordFailedReportsAPlainRefusal(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := writeTestMarketplaceDir(t, "market-a")
	if _, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceDirectory, Path: src}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	original := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = original })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	err := m.RefreshMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RefreshMarketplace = nil, want the failed record reported")
	}
	if errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want a plain refusal: a directory source's refresh touches nothing on disk", err)
	}
}

// A fetched (git) source's refresh touches the clone on disk first - a pull,
// a staged reclone, or a first fetch - and only then records it. A failing
// record leaves the store's directories changed and its file not naming
// them, so the hub must still announce it.
func TestRefreshGitSourceWhoseRecordFailedReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	installLoc := m.marketplaceDir("market-a")
	if err := os.MkdirAll(installLoc, 0o755); err != nil {
		t.Fatalf("planting the clone directory: %v", err)
	}
	if err := m.saveMarketplaces(Marketplaces{"market-a": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"},
		InstallLocation: installLoc,
	}}); err != nil {
		t.Fatalf("planting the marketplace: %v", err)
	}
	origPull := marketplaceGitPull
	t.Cleanup(func() { marketplaceGitPull = origPull })
	marketplaceGitPull = func(context.Context, string) error { return nil }

	original := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = original })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	err := m.RefreshMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RefreshMarketplace = nil, want the failed record reported")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed so the hub still broadcasts: the pull already ran", err)
	}
}

// A removal whose registry write fails must leave the plugin installed, files
// and entry both: deleting the cache first made the failure unrecoverable — the
// registry still named a plugin whose files were gone — and left a change no
// rollback could undo. Saving first makes the failure a plain refusal.
func TestRemoveWhoseRegistryWriteFailedKeepsThePluginInstalled(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	// An install inside the cache directory, which is the only kind Remove
	// deletes (a directory-source install points at the source itself, so it
	// would not exercise this path at all — measured).
	installed := filepath.Join(m.cacheDir(), "demo")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatalf("planting the install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installed, "marker"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("planting the install payload: %v", err)
	}
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
		registryKey("demo", "market-a"): {{
			InstallPath: installed,
			Version:     "1",
			Enabled:     true,
			Source:      Source{Kind: SourceDirectory, Path: installed},
		}},
	}}); err != nil {
		t.Fatalf("planting the registry: %v", err)
	}

	original := installSaveRegistry
	t.Cleanup(func() { installSaveRegistry = original })
	installSaveRegistry = func(string, Registry) error {
		return errors.New("the registry could not be written")
	}

	removeErr := m.Remove(ctx, "demo", "market-a")
	installSaveRegistry = original
	if removeErr == nil {
		t.Fatal("Remove = nil, want the failed registry write reported")
	}
	if errors.Is(removeErr, ErrStoreChanged) {
		t.Fatalf("Remove = %v, want a refusal that changed nothing", removeErr)
	}
	if _, statErr := os.Stat(filepath.Join(installed, "marker")); statErr != nil {
		t.Fatalf("the plugin's files are gone (%v), but the registry still lists it", statErr)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry after the refused removal: %v", err)
	}
	if len(reg.Plugins[registryKey("demo", "market-a")]) != 1 {
		t.Fatalf("registry = %+v, want the plugin still installed after a refused removal", reg.Plugins)
	}
}

// AddMarketplace clones a non-directory source into place before it saves
// the metadata. A save failure must roll that clone back: the name was never
// recorded, so a clone left behind would be a directory the store knows
// nothing about.
func TestAddMarketplaceWhoseSaveFailedRollsBackTheClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")

	original := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = original })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	_, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src})
	if err == nil {
		t.Fatal("AddMarketplace = nil, want the failed save reported")
	}
	if errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want a plain refusal: the rollback removed the clone", err)
	}
	if _, statErr := os.Stat(m.marketplaceDir("market-a")); !os.IsNotExist(statErr) {
		t.Fatalf("clone survived a rolled-back Add: %v", statErr)
	}
}

// When the rollback itself cannot remove the clone, the store is left
// changed after all: a directory nothing lists now occupies the name's slot.
func TestAddMarketplaceWhoseRollbackAlsoFailedReportsTheStoreChanged(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")

	originalWrite := marketplaceAtomicWriteFile
	originalRemove := marketplaceRemoveAll
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = originalWrite
		marketplaceRemoveAll = originalRemove
	})
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}
	marketplaceRemoveAll = func(path string) error {
		if path == m.marketplaceDir("market-a") {
			return errors.New("the rollback removal failed")
		}
		return originalRemove(path)
	}

	_, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src})
	if err == nil {
		t.Fatal("AddMarketplace = nil, want the failed save reported")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed: the rollback itself failed", err)
	}
	// Both underlying failures have to survive: the save error is why a
	// rollback was attempted at all, and the rollback error is why it did
	// not undo anything. Dropping either would tell the caller only half of
	// what happened.
	if !strings.Contains(err.Error(), "the store file could not be written") {
		t.Fatalf("err = %v, want the save failure that triggered the rollback", err)
	}
	if !strings.Contains(err.Error(), "the rollback removal failed") {
		t.Fatalf("err = %v, want the rollback failure reported too", err)
	}
}

// ensureFetched's lazy clone (a seeded marketplace's first access) can
// succeed and leave a directory on disk before the metadata save that would
// record it fails - the same shape AddMarketplace has. A rollback that
// succeeds is then a clean refusal.
func TestEnsureFetchedWhoseSaveFailedRollsBackTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if err := m.saveMarketplaces(Marketplaces{"market-a": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"}}}); err != nil {
		t.Fatal(err)
	}

	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error { return os.MkdirAll(dest, 0o755) }

	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	_, _, err := m.ensureFetched(ctx, "market-a")
	if err == nil {
		t.Fatal("ensureFetched = nil, want the failed save reported")
	}
	if errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want a plain refusal: the rollback removed the clone", err)
	}
	if _, statErr := os.Stat(m.marketplaceDir("market-a")); !os.IsNotExist(statErr) {
		t.Fatalf("clone survived a rolled-back fetch: %v", statErr)
	}
}

// When the rollback itself cannot remove the clone, the store is left
// changed after all - and both the save failure and the rollback failure
// have to survive in the error, or the caller only learns half of what
// happened.
func TestEnsureFetchedWhoseRollbackAlsoFailedReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	if err := m.saveMarketplaces(Marketplaces{"market-a": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"}}}); err != nil {
		t.Fatal(err)
	}

	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error { return os.MkdirAll(dest, 0o755) }

	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	origRemove := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = origRemove })
	marketplaceRemoveAll = func(path string) error {
		if path == m.marketplaceDir("market-a") {
			return errors.New("the rollback removal failed")
		}
		return origRemove(path)
	}

	_, _, err := m.ensureFetched(ctx, "market-a")
	if err == nil {
		t.Fatal("ensureFetched = nil, want the failed save reported")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed: the rollback itself failed", err)
	}
	if !strings.Contains(err.Error(), "the store file could not be written") {
		t.Fatalf("err = %v, want the save failure that triggered the rollback", err)
	}
	if !strings.Contains(err.Error(), "the rollback removal failed") {
		t.Fatalf("err = %v, want the rollback failure reported too", err)
	}
}

// RemoveMarketplace must save the metadata before it deletes the clone: a
// save that fails first leaves the marketplace registered and its clone
// untouched, a plain refusal rather than a change no rollback could undo.
func TestRemoveMarketplaceWhoseSaveFailedLeavesTheClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	ctx := context.Background()
	src := makeMarketplaceRepo(t, "market-a")
	ref, err := m.AddMarketplace(ctx, "market-a", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	original := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = original })
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}

	err = m.RemoveMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RemoveMarketplace = nil, want the failed save reported")
	}
	if errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want a plain refusal: the clone was never touched", err)
	}
	if _, statErr := os.Stat(ref.InstallLocation); statErr != nil {
		t.Fatalf("clone deleted before the save that names it succeeded: %v", statErr)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatalf("loadMarketplaces after the refused removal: %v", err)
	}
	if _, ok := mk["market-a"]; !ok {
		t.Fatal("marketplace no longer registered after a refused removal")
	}
}

// A clone delete that fails after the save that removes it from the
// marketplaces file has already applied: the listing is already stale, so
// the hub still owes every other client the broadcast even though the clone
// itself is litter RemoveMarketplace could not clean up.
func TestRemoveMarketplaceWhoseCloneDeleteFailedReportsTheStoreChanged(t *testing.T) {
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

	original := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = original })
	marketplaceRemoveAll = func(path string) error {
		if path == m.marketplaceDir("market-a") {
			return errors.New("the clone could not be removed")
		}
		return original(path)
	}

	err := m.RemoveMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RemoveMarketplace = nil, want the failed clone removal reported")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed: the save already applied", err)
	}
	mk, loadErr := m.loadMarketplaces()
	if loadErr != nil {
		t.Fatalf("loadMarketplaces after the applied removal: %v", loadErr)
	}
	if _, ok := mk["market-a"]; ok {
		t.Fatal("marketplace still registered after a removal whose save succeeded")
	}
}

// The clone-delete failure's error reaches the RPC caller as a wire error;
// this machine's absolute plugin-store path is server-side detail (a
// warning), not something a client needs or should see.
func TestRemoveMarketplaceWhoseCloneDeleteFailedNamesNoAbsolutePath(t *testing.T) {
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

	original := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = original })
	marketplaceRemoveAll = func(path string) error {
		if path == m.marketplaceDir("market-a") {
			return errors.New("the clone could not be removed")
		}
		return original(path)
	}

	err := m.RemoveMarketplace(ctx, "market-a")
	if err == nil {
		t.Fatal("RemoveMarketplace = nil, want the failed clone removal reported")
	}
	if strings.Contains(err.Error(), m.marketplaceDir("market-a")) {
		t.Fatalf("err = %v, want no absolute path in the client-facing error", err)
	}
	if !strings.Contains(err.Error(), "market-a") {
		t.Fatalf("err = %v, want the marketplace named", err)
	}
}

// Install's first access to a seeded, unfetched marketplace (catalogPlugin ->
// ensureFetched) persists InstallLocation/LastUpdated before it even knows
// whether the plugin it was asked for exists in the catalog. A plugin that
// turns out missing leaves the marketplace listing changed regardless of
// whether the plugin itself ever installs.
func TestInstallWhoseCatalogLookupFailedReportsTheMarketplaceStoreChanged(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	// Seed a pointer (empty InstallLocation) rather than AddMarketplace, so
	// Install's own access is the lazy fetch this test is about.
	if err := m.saveMarketplaces(Marketplaces{name: {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}

	_, marketplaceChanged, err := m.Install(context.Background(), "does-not-exist", name)
	if err == nil {
		t.Fatal("Install(missing plugin) = nil, want the missing plugin reported")
	}
	if !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("Install(missing plugin) = %v, want ErrPluginNotFound", err)
	}
	if !marketplaceChanged {
		t.Fatal("Install(missing plugin) marketplaceChanged = false, want true: the lazy fetch already persisted")
	}
	mk, listErr := m.ListMarketplaces(context.Background())
	if listErr != nil {
		t.Fatalf("ListMarketplaces: %v", listErr)
	}
	if mk[name].InstallLocation == "" {
		t.Fatal("InstallLocation not backfilled by the lazy fetch")
	}
}

// The sibling case: the same failure on an already-fetched marketplace never
// touched the marketplace store, so it is a plain refusal.
func TestInstallWhoseCatalogLookupFailedOnAnAlreadyFetchedMarketplaceIsAPlainRefusal(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), name, Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	_, marketplaceChanged, err := m.Install(context.Background(), "does-not-exist", name)
	if err == nil {
		t.Fatal("Install(missing plugin) = nil, want the missing plugin reported")
	}
	if marketplaceChanged {
		t.Fatal("Install(missing plugin) marketplaceChanged = true, want false: the marketplace was already fetched")
	}
}

// The Upgrade sibling of the case above: Upgrade's own catalogPlugin call can
// lazily fetch too (a seeded marketplace re-pointed since install), and a
// registry save failure after that fetch must still report the marketplace
// change even though the upgrade itself never applied.
func TestUpgradeWhoseSaveFailedAfterALazyFetchReportsTheMarketplaceStoreChanged(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// A separately git-backed plugin, so advancing it gives Upgrade a real
	// sha change to act on (a plugin referenced relative to the marketplace
	// repo has no sha of its own, and upgradeLocked no-ops when the sha is
	// unchanged - measured).
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")
	const name = "acme"
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), name, Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	// Re-seed InstallLocation empty, as if the marketplace had been dropped to
	// a pointer again (EditMarketplace re-sourcing, or a store repair): the
	// installed entry stays, but Upgrade's own catalogPlugin call must fetch
	// the marketplace again.
	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref := mk[name]
	ref.InstallLocation = ""
	if err := m.saveMarketplaces(Marketplaces{name: ref}); err != nil {
		t.Fatalf("re-seeding the pointer: %v", err)
	}

	original := installSaveRegistry
	t.Cleanup(func() { installSaveRegistry = original })
	installSaveRegistry = func(string, Registry) error {
		return errors.New("the registry could not be written")
	}

	_, marketplaceChanged, err := m.Upgrade(context.Background(), "widget", name)
	if err == nil {
		t.Fatal("Upgrade = nil, want the failed registry save reported")
	}
	if !marketplaceChanged {
		t.Fatal("Upgrade marketplaceChanged = false, want true: the lazy fetch already persisted")
	}
}

// writeTestMarketplaceDir plants a directory-source marketplace holding one
// plugin, which needs no network and no git binary.
func writeTestMarketplaceDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"` + name + `","plugins":[{"name":"demo","source":"./demo","description":"d"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("marketplace.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo", ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", ".claude-plugin", "plugin.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	return dir
}
