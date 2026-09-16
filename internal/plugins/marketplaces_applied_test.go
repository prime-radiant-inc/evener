package plugins

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

// A rename that leaves the store between the two names has changed the store:
// the registry keys this marketplace's plugins under the new name while the
// marketplaces file still records the old one. Whoever called it must be able
// to tell that from a failure that rolled back, because the hub announces an
// applied write to every other client and a rolled-back one is not one
// (#1543/#1572's rule, and the reason ErrStoreChanged is exported).
func TestAnIncompleteRenameRollbackReportsTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if to == filepath.Join(m.cacheDir(), "a-b") || from == m.marketplaceDir("a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the cache move to fail")
	}
	if !errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want it to report the store changed so the hub still broadcasts", err)
	}
}

// A refusal changed nothing, so it must not read as a changed store: the hub
// would have every client refetch a listing that is still correct.
func TestARefusalDoesNotReportTheStoreChanged(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard

	if err := m.RemoveMarketplace(context.Background(), "nowhere"); err == nil {
		t.Fatal("RemoveMarketplace(nowhere) = nil, want a refusal")
	} else if errors.Is(err, ErrStoreChanged) {
		t.Fatalf("err = %v, want a refusal that leaves the store as it was", err)
	}
}
