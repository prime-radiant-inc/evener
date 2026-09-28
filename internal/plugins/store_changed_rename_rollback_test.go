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
// The migration barrier's moveMarketplace has moved the clone to the new name
// and fails moving the plugin cache; its own undo cannot put the clone back, so
// no store file is ever written and only the rename site can report the state.
func TestAnIncompleteRenameRollbackReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	reports := recordStoreChanges(m)
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if to == filepath.Join(m.cacheDir(), "a-b") || from == m.marketplaceDir("a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	if _, err := m.ListMarketplaces(context.Background()); err == nil {
		t.Fatal("expected the cache move to fail")
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

// The rename moves the plugin cache as well as the clone, and each has its own
// undo step. This pins the cache's step on its own: the clone is put back
// cleanly, but the cache cannot be, so the store is still left between the
// names and the hook must report it. Without this, only the clone's call site
// would be covered.
func TestAnIncompleteCacheRenameRollbackReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "market-a", "widget")
	reports := recordStoreChanges(m)

	originalWrite := marketplaceAtomicWriteFile
	originalRename := marketplaceRename
	t.Cleanup(func() {
		marketplaceAtomicWriteFile = originalWrite
		marketplaceRename = originalRename
	})
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("the store file could not be written")
	}
	// The cache's own undo restores newCache back to the old name; refuse only
	// that, so the clone's undo still succeeds and the failure is the cache's.
	newCache := filepath.Join(m.cacheDir(), "market-b")
	marketplaceRename = func(from, to string) error {
		if from == newCache {
			return errors.New("cache restore refused")
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
		t.Errorf("reported %+v, want Marketplaces true: the cache could not be put back, so the store is left between the names", got)
	}
}
