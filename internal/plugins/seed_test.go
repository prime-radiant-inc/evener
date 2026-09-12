package plugins

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

func TestSeedDefaultMarketplaces_FirstRunOnly(t *testing.T) {
	m := NewManager(t.TempDir())

	seeded, err := m.SeedDefaultMarketplaces(context.Background())
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
	seeded, err = m.SeedDefaultMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if seeded {
		t.Fatal("second run should NOT re-seed")
	}

	// a user who removes a seeded marketplace and re-runs must not get it back
	if err := m.RemoveMarketplace(context.Background(), "superpowers-marketplace"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := m.SeedDefaultMarketplaces(context.Background()); err != nil {
		t.Fatalf("third seed: %v", err)
	}
	mk, _ = m.ListMarketplaces()
	if _, ok := mk["superpowers-marketplace"]; ok {
		t.Fatal("removed seed was re-added")
	}
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

// A store root that is not an absolute path — none could be resolved at all,
// or one that names a directory relative to wherever the process happens to be
// — leaves every path under it relative. Seeding would then write
// known_marketplaces.json and take its lock in the working directory, which is
// somebody's project. Every launch path seeds on the way past, so refusing is
// the whole answer: the callers already carry a seeding failure as a warning.
func TestSeedDefaultMarketplaces_RefusesAStoreRootThatIsNotResolved(t *testing.T) {
	tests := []struct {
		name    string
		manager func(t *testing.T) *Manager
		wantErr string
	}{
		{
			name: "no root could be resolved",
			manager: func(t *testing.T) *Manager {
				t.Setenv(envvars.XDGConfigHome.Name, "")
				restoreHome := pluginUserHomeDir
				pluginUserHomeDir = func() (string, error) { return "", errors.New("no home directory") }
				t.Cleanup(func() { pluginUserHomeDir = restoreHome })
				return NewManager("")
			},
			wantErr: "no plugin store root is configured",
		},
		{
			name:    "the root names a relative directory",
			manager: func(*testing.T) *Manager { return NewManager("store") },
			wantErr: "not an absolute path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)

			seeded, err := test.manager(t).SeedDefaultMarketplaces(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("SeedDefaultMarketplaces error = %v, want %q", err, test.wantErr)
			}
			if seeded {
				t.Error("reported a seed against a root it refused")
			}
			entries, err := os.ReadDir(cwd)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("seeding wrote %v into the working directory", entries)
			}
		})
	}
}
