package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recordStoreChanges installs an OnStoreChanged callback on m that appends
// every report it receives to the returned slice's backing pointer.
func recordStoreChanges(m *Manager) *[]StoreChanged {
	reports := &[]StoreChanged{}
	m.OnStoreChanged(func(c StoreChanged) { *reports = append(*reports, c) })
	return reports
}

// TestSaveRegistry_MarksPluginsChangedOnSuccessOnly is saveRegistry's own
// primitive test: it marks the Manager's current session Plugins-changed the
// instant its own write lands, and marks nothing when that write fails.
func TestSaveRegistry_MarksPluginsChangedOnSuccessOnly(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard

	if err := m.saveRegistry(Registry{Version: 2, Plugins: map[string][]InstallEntry{}}); err != nil {
		t.Fatalf("saveRegistry: %v", err)
	}
	if !m.pendingStoreChanged.Plugins {
		t.Error("a successful saveRegistry did not mark Plugins changed")
	}
	if m.pendingStoreChanged.Marketplaces {
		t.Error("saveRegistry marked Marketplaces changed too")
	}

	m.pendingStoreChanged = StoreChanged{}
	orig := installSaveRegistry
	installSaveRegistry = func(string, Registry) error { return errors.New("boom") }
	t.Cleanup(func() { installSaveRegistry = orig })
	if err := m.saveRegistry(Registry{}); err == nil {
		t.Fatal("expected saveRegistry to fail")
	}
	if m.pendingStoreChanged.Plugins {
		t.Error("a failed saveRegistry marked Plugins changed")
	}
}

// TestSaveMarketplaces_MarksMarketplacesChangedOnSuccessOnly is
// saveMarketplaces' own primitive test, the marketplaces-file sibling of
// TestSaveRegistry_MarksPluginsChangedOnSuccessOnly.
func TestSaveMarketplaces_MarksMarketplacesChangedOnSuccessOnly(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard

	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatalf("saveMarketplaces: %v", err)
	}
	if !m.pendingStoreChanged.Marketplaces {
		t.Error("a successful saveMarketplaces did not mark Marketplaces changed")
	}
	if m.pendingStoreChanged.Plugins {
		t.Error("saveMarketplaces marked Plugins changed too")
	}

	m.pendingStoreChanged = StoreChanged{}
	orig := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })
	if err := m.saveMarketplaces(Marketplaces{}); err == nil {
		t.Fatal("expected saveMarketplaces to fail")
	}
	if m.pendingStoreChanged.Marketplaces {
		t.Error("a failed saveMarketplaces marked Marketplaces changed")
	}
}

// TestSaveRename_MarksBothOnSuccess is saveRename's own primitive test: it
// writes the registry then the marketplaces file, so a successful call marks
// both flags — composed entirely from the two hooks above, with no hook of
// its own.
func TestSaveRename_MarksBothOnSuccess(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{}}
	mk := Marketplaces{}
	if err := m.saveRename(mk, "old", "new", MarketplaceRef{}, reg, reg); err != nil {
		t.Fatalf("saveRename: %v", err)
	}
	if !m.pendingStoreChanged.Plugins || !m.pendingStoreChanged.Marketplaces {
		t.Errorf("saveRename pendingStoreChanged = %+v, want both true", m.pendingStoreChanged)
	}
}

// TestLockStore_ReportsAccumulatedChangeOnceAfterRelease drives the mechanism
// end to end through Install: the callback fires exactly once, after release
// has already run, with only Plugins set (a plugin install never touches
// known_marketplaces.json once the marketplace itself is already fetched).
func TestLockStore_ReportsAccumulatedChangeOnceAfterRelease(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	reports := recordStoreChanges(m)
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(*reports) != 1 {
		t.Fatalf("OnStoreChanged fired %d times, want 1: %+v", len(*reports), *reports)
	}
	if got := (*reports)[0]; !got.Plugins || got.Marketplaces {
		t.Errorf("reported %+v, want {Plugins:true Marketplaces:false}", got)
	}
}

// TestLockStore_NoCallbackWhenNothingWritten proves the other half: a lock
// session that writes nothing invokes the installed callback zero times, not
// once with a zero StoreChanged. Gc is the store's own example of a
// lockStore caller that only ever reads and removes orphaned cache dirs —
// never installed_plugins.json or known_marketplaces.json.
func TestLockStore_NoCallbackWhenNothingWritten(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	reports := recordStoreChanges(m)
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(*reports) != 0 {
		t.Fatalf("OnStoreChanged fired on a no-op Gc: %+v", *reports)
	}
}

// TestAddMarketplace_SwapSucceedsThenSaveFailsReportsNoChange is the
// regression this design's scope decision rests on. swapInClone renames the
// new clone into place — a real directory change no primitive-level hook
// marks — and then saveMarketplaces fails, so AddMarketplace removes the
// swapped-in clone again (its own cleanup, not this package's undo
// machinery) and returns an error. Nothing was ever written to
// known_marketplaces.json, and nothing List or ListMarketplaces could ever
// have read changed either: the callback must not fire.
func TestAddMarketplace_SwapSucceedsThenSaveFailsReportsNoChange(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, _ := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	reports := recordStoreChanges(m)

	orig := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })

	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err == nil {
		t.Fatal("expected AddMarketplace to fail")
	}
	if len(*reports) != 0 {
		t.Fatalf("OnStoreChanged fired after a swap that was then rolled back: %+v", *reports)
	}
	if _, err := os.Stat(m.marketplaceDir("acme")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapped-in clone was not rolled back: stat = %v", err)
	}
}

// TestMigrateMarketplaceName_MoveSucceedsThenSaveFailsReportsNoChange covers
// the same shape one level deeper: the migration barrier moves a refused
// name's clone and cache directories (moveMarketplace, itsOwn branch) before
// the rename's own saveRename call, so a persistent failure of that save
// still leaves the callback silent once the move is undone.
func TestMigrateMarketplaceName_MoveSucceedsThenSaveFailsReportsNoChange(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	reports := recordStoreChanges(m)

	orig := installSaveRegistry
	installSaveRegistry = func(string, Registry) error { return errors.New("boom") }
	t.Cleanup(func() { installSaveRegistry = orig })

	if _, err := m.ListMarketplaces(context.Background()); err == nil {
		t.Fatal("expected the migration's save to fail")
	}
	if len(*reports) != 0 {
		t.Fatalf("OnStoreChanged fired after a rename whose save failed and rolled back: %+v", *reports)
	}
	mustNotExist(t, m.marketplaceDir("foo-bar"))
}

// TestMigrateMarketplaceNames_PersistentPreWriteFailureNeverReportsChange
// proves a store needing migration, with every write persistently failing,
// never reports a change no matter how many times it is listed — not once,
// up front, before any write is attempted.
func TestMigrateMarketplaceNames_PersistentPreWriteFailureNeverReportsChange(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	reports := recordStoreChanges(m)

	orig := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })

	for i := range 2 {
		if _, err := m.ListMarketplaces(context.Background()); err == nil {
			t.Fatalf("call %d: expected ListMarketplaces to fail", i)
		}
	}
	if len(*reports) != 0 {
		t.Fatalf("a persistently failing migration reported change: %+v", *reports)
	}
}

// TestManager_ConcurrentLockSessionsDoNotRaceStoreChanged proves
// pendingStoreChanged/onStoreChanged need their own synchronization, not
// just the file lock's: flock gives real mutual exclusion in wall-clock time
// but no Go happens-before edge the race detector can see, and several of
// this package's own lockAcquirer test seams (installAcquireLock,
// marketplaceAcquireLock, gcAcquireLock) are stubbed to a no-op release in
// other tests. Several goroutines each add a distinct marketplace to the one
// Manager concurrently; every one of them must be reported, and -race must
// find nothing.
func TestManager_ConcurrentLockSessionsDoNotRaceStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard

	const n = 8
	dirs := make([]string, n)
	for i := range dirs {
		dirs[i] = plantCatalog(t, t.TempDir())
	}

	var mu sync.Mutex
	var got []StoreChanged
	m.OnStoreChanged(func(c StoreChanged) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("mkt-%d", i)
			if _, err := m.AddMarketplace(context.Background(), name, Source{Kind: SourceDirectory, Path: dirs[i]}); err != nil {
				t.Errorf("AddMarketplace %s: %v", name, err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("OnStoreChanged fired %d times, want %d: %+v", len(got), n, got)
	}
	for _, c := range got {
		if !c.Marketplaces {
			t.Errorf("report %+v missing Marketplaces", c)
		}
	}
}

// seedUnfetchedPointer writes known_marketplaces.json with a registered but
// unfetched pointer to mktRepo (empty InstallLocation), the shape
// SeedDefaultMarketplaces leaves behind, and clears the pending StoreChanged
// the direct save itself accumulated so the next lock session starts clean.
func seedUnfetchedPointer(t *testing.T, m *Manager, name, mktRepo string) {
	t.Helper()
	if err := m.saveMarketplaces(Marketplaces{name: {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}
	m.pendingStoreChanged = StoreChanged{}
}

// TestBrowse_LazyFetchPersistsAndReportsMarketplacesChanged pins the behavior
// issue #1672 reported missing: Browse on a seeded, unfetched marketplace
// clones it lazily — persisting known_marketplaces.json's InstallLocation — and
// that write must reach every client through the post-commit hook, exactly
// once, as a Marketplaces change.
func TestBrowse_LazyFetchPersistsAndReportsMarketplacesChanged(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	seedUnfetchedPointer(t, m, name, mktRepo)

	reports := recordStoreChanges(m)
	if _, err := m.Browse(context.Background(), name); err != nil {
		t.Fatalf("Browse on seeded-but-unfetched marketplace: %v", err)
	}
	if len(*reports) != 1 {
		t.Fatalf("OnStoreChanged fired %d times, want 1: %+v", len(*reports), *reports)
	}
	if got := (*reports)[0]; !got.Marketplaces || got.Plugins {
		t.Errorf("reported %+v, want {Plugins:false Marketplaces:true}", got)
	}
	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mk[name].InstallLocation == "" {
		t.Fatal("InstallLocation not backfilled by Browse's lazy fetch")
	}
}

// TestBrowse_LazyFetchPersistsThenLaterStepFailsStillReports is the exact
// shape issue #1672 named: the lazy fetch persists marketplace metadata and a
// later step (here ParseCatalog) fails, so Browse returns an error — yet the
// applied write must still be reported, or other clients keep showing the
// marketplace as unfetched.
func TestBrowse_LazyFetchPersistsThenLaterStepFailsStillReports(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "mkt-broken")
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, dir, "README.md", "broken catalog")

	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	seedUnfetchedPointer(t, m, "acme", dir)

	reports := recordStoreChanges(m)
	if _, err := m.Browse(context.Background(), "acme"); err == nil {
		t.Fatal("expected Browse to fail on the broken catalog")
	}
	if len(*reports) != 1 {
		t.Fatalf("OnStoreChanged fired %d times, want 1: %+v", len(*reports), *reports)
	}
	if got := (*reports)[0]; !got.Marketplaces || got.Plugins {
		t.Errorf("reported %+v, want {Plugins:false Marketplaces:true}", got)
	}
}
