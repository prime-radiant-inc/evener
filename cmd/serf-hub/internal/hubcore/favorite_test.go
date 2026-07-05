package hubcore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFavoriteStoreSetAndDelete(t *testing.T) {
	dir := t.TempDir()
	fav := NewFavoriteStore(filepath.Join(dir, "index.db"))
	now := time.Unix(1_700_000_000, 0)
	if err := fav.Set("session", "01A", true, now); err != nil {
		t.Fatal(err)
	}
	got, err := fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !got[ArchiveKey{Kind: "session", ID: "01A"}] {
		t.Fatalf("favorite not persisted: %v", got)
	}
	if err := fav.Delete("session", "01A"); err != nil {
		t.Fatal(err)
	}
	got, _ = fav.Favorites()
	if _, present := got[ArchiveKey{Kind: "session", ID: "01A"}]; present {
		t.Fatalf("row should be gone after Delete: %v", got)
	}
}
