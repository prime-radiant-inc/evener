package plugins

import (
	"context"
	"os"
	"testing"
)

func TestSeedDefaultMarketplaces_FirstRunOnly(t *testing.T) {
	m := NewManager(t.TempDir())

	seeded, err := m.SeedDefaultMarketplaces()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !seeded {
		t.Fatal("first run should seed")
	}
	mk, _ := m.ListMarketplaces()
	if _, ok := mk["claude-plugins-official"]; !ok {
		t.Fatalf("official marketplace not seeded: %v", mk)
	}
	if _, ok := mk["superpowers-marketplace"]; !ok {
		t.Fatalf("superpowers marketplace not seeded: %v", mk)
	}

	// second run: no-op (respects user removals)
	seeded, err = m.SeedDefaultMarketplaces()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if seeded {
		t.Fatal("second run should NOT re-seed")
	}

	// a user who removes a seeded marketplace and re-runs must not get it back
	if err := m.RemoveMarketplace("superpowers-marketplace"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m.SeedDefaultMarketplaces()
	mk, _ = m.ListMarketplaces()
	if _, ok := mk["superpowers-marketplace"]; ok {
		t.Fatal("removed seed was re-added")
	}
	_ = os.Stat
}

func TestBrowse_LazyFetchesSeededPointer(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// a real local git marketplace, registered as an unfetched pointer
	mktRepo := makeMarketplaceRepo(t, "acme") // helper in marketplaces_test.go: builds a git repo with marketplace.json name "acme"
	m := NewManager(t.TempDir())
	// seed a pointer (empty InstallLocation) directly
	if err := m.saveMarketplaces(Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}
	cat, err := m.Browse(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Browse lazy-fetch: %v", err)
	}
	if cat.Name != "acme" {
		t.Fatalf("catalog = %+v", cat)
	}
	// InstallLocation now backfilled
	mk, _ := m.ListMarketplaces()
	if mk["acme"].InstallLocation == "" {
		t.Fatal("InstallLocation not backfilled after lazy fetch")
	}
}
