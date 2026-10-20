package plugins

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A rename that leaves the store between the two names has changed the store:
// the directory is still under the new name while known_marketplaces.json
// records the old one, so every other client's cached listing is stale the
// moment this lock session gives up. OnStoreChanged is the only path that tells
// them so (#1800, ported from #1602's
// TestAnIncompleteRenameRollbackReportsTheStoreChanged).
//
// Automatic migration moves only the clone. A failed marketplace save and a
// failed clone rollback must still report the surviving change to observers.
func TestAnIncompleteRenameRollbackReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	reports := recordStoreChanges(m)
	orig, write := marketplaceRename, marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceRename, marketplaceAtomicWriteFile = orig, write })
	marketplaceAtomicWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			return os.ErrPermission
		}
		return write(path, data, mode)
	}
	marketplaceRename = func(from, to string) error {
		if from == m.marketplaceDir("a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	if _, err := m.ListMarketplaces(context.Background()); err == nil {
		t.Fatal("expected the save and clone rollback to fail")
	}
	if len(*reports) != 1 {
		t.Fatalf("OnStoreChanged fired %d times, want 1: %+v", len(*reports), *reports)
	}
	if got := (*reports)[0]; !got.Marketplaces {
		t.Errorf("reported %+v, want Marketplaces true: the clone could not be put back, so the store is left between the names", got)
	}
}

// An edit renames directories before it writes the store files. When the write
// fails AND the undo that would move them back fails too, the store is left
// changed — the clone sits under the new name while known_marketplaces.json
// still records the old one (#1800, ported from #1602's
// TestEditWhoseUndoFailedReportsTheStoreChanged).
func TestEditWhoseUndoFailedReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	// A clone-backed marketplace: its install location is a directory inside
	// the store, which is what a rename actually moves. A directory-source
	// marketplace points outside the store and moves nothing, so it cannot
	// reach this state at all (measured).
	plantLegacyMarketplace(t, m, "market-a", "widget")
	reports := recordStoreChanges(m)

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
	path := m.marketplaceDir("market-a")
	marketplaceRename = func(from, to string) error {
		if to == path {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("permission denied")}
		}
		return originalRename(from, to)
	}

	if _, err := m.EditMarketplace(context.Background(), "market-a", "market-b", nil); err == nil {
		t.Fatal("EditMarketplace = nil, want the failed write reported")
	}
	if len(*reports) != 1 {
		t.Fatalf("OnStoreChanged fired %d times, want 1: %+v", len(*reports), *reports)
	}
	if got := (*reports)[0]; !got.Marketplaces {
		t.Errorf("reported %+v, want Marketplaces true: the clone is under the new name while the file still names the old", got)
	}
}

// Explicit rename has no cache undo. A failed first save with successful clone
// rollback changes neither store file, emits no change, and preserves packages.
func TestExplicitRenameSaveFailureLeavesCacheAndStoreUnchanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	trees := plantMigrationRevisions(t, m, "market-a", "widget")
	mkBefore, regBefore := readStoreFile(t, m.marketplacesFile()), readStoreFile(t, m.registryPath())
	reports := recordStoreChanges(m)
	originalSave, originalRename := installSaveRegistry, marketplaceRename
	t.Cleanup(func() { installSaveRegistry, marketplaceRename = originalSave, originalRename })
	installSaveRegistry = func(string, Registry) error { return os.ErrPermission }
	cacheMoves := 0
	marketplaceRename = func(from, to string) error {
		if pathWithinDir(m.cacheDir(), from) || pathWithinDir(m.cacheDir(), to) {
			cacheMoves++
			return os.ErrPermission
		}
		return originalRename(from, to)
	}
	if _, err := m.EditMarketplace(context.Background(), "market-a", "market-b", nil); err == nil {
		t.Fatal("expected failed registry save")
	}
	if cacheMoves != 0 || len(*reports) != 0 {
		t.Fatalf("cache moves=%d, unexpected changes=%+v", cacheMoves, *reports)
	}
	if readStoreFile(t, m.marketplacesFile()) != mkBefore || readStoreFile(t, m.registryPath()) != regBefore {
		t.Fatal("failed save changed store")
	}
	if _, err := os.Stat(m.marketplaceDir("market-a")); err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
}

// A blocked name used to move individual plugin caches out from under the
// marketplace owning its parent. Automatic migration now leaves both trees
// in place, so there can be no partial cache-move rollback to report.
func TestAutomaticMigrationDoesNotMoveBlockedPluginCaches(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	owner := plantLegacyMarketplace(t, m, "a", "owner")
	alpha := plantLegacyMarketplace(t, m, "a/b", "alpha")
	beta := plantLegacyMarketplace(t, m, "a/b", "beta")
	trees := []migrationTree{snapshotMigrationTree(t, owner), snapshotMigrationTree(t, alpha), snapshotMigrationTree(t, beta)}
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	calls := 0
	marketplaceRename = func(from, to string) error {
		if pathWithinDir(m.cacheDir(), from) || pathWithinDir(m.cacheDir(), to) {
			calls++
			return os.ErrPermission
		}
		return orig(from, to)
	}
	if _, err := m.Gc(context.Background()); err != nil || calls != 0 {
		t.Fatalf("automatic migration err=%v, cache rename attempts=%d", err, calls)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if installedAt(t, reg, "alpha@a-b").InstallPath != alpha || installedAt(t, reg, "beta@a-b").InstallPath != beta {
		t.Fatal("blocked-name migration changed an install path")
	}
}
