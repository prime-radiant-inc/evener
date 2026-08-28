package plugins

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultRoot_UsesXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got := DefaultRoot()
	want := filepath.Join(xdg, "evener", "plugins")
	if got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestManagerPaths(t *testing.T) {
	m := NewManager("/store")
	cases := map[string]string{
		m.registryPath():                         "/store/installed_plugins.json",
		m.marketplacesDir():                      "/store/marketplaces",
		m.cacheDir():                             "/store/cache",
		m.lockPath():                             "/store/.lock",
		m.bundledDir():                           "/store/bundled",
		m.bundledLockPath():                      "/store/bundled/.lock",
		m.marketplaceDir("acme"):                 "/store/marketplaces/acme",
		m.pluginCacheDir("acme", "widget", "ab"): "/store/cache/acme/widget/ab",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

// plantAmbientStore writes the two files a store keeps at its root into dir,
// each naming something a reader would hand back. A reader that derived its
// path from an unresolved root would find these and answer with them.
func plantAmbientStore(t *testing.T, dir string) {
	t.Helper()
	registry := `{"version":2,"plugins":{"ambient@ambient":[{"installPath":"/nowhere","version":"9.9.9","enabled":true}]}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	marketplaces := `{"ambient":{"source":{"source":"github","repo":"ambient/ambient"},"installLocation":"/nowhere","lastUpdated":"2026-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(dir, "known_marketplaces.json"), []byte(marketplaces), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Reading the store is as rooted as writing it. The store lock is the writers'
// choke point and cannot help a reader that never takes one: with an empty or
// relative root, List and ListMarketplaces derived a path relative to whatever
// directory the process happened to be in and handed back that directory's
// installed_plugins.json or known_marketplaces.json as the user's store. Every
// store path is derived through storePath, so a reader refuses by construction
// rather than by remembering.
func TestStoreReaders_RefuseARootThatIsNotResolved(t *testing.T) {
	readers := []struct {
		name string
		read func(*Manager) (int, error)
	}{
		{"List", func(m *Manager) (int, error) {
			items, err := m.List()
			return len(items), err
		}},
		{"ListMarketplaces", func(m *Manager) (int, error) {
			mk, err := m.ListMarketplaces()
			return len(mk), err
		}},
	}
	roots := []struct {
		name    string
		root    string
		wantErr string
	}{
		{"no root could be resolved", "", "no plugin store root is configured"},
		{"the root names a relative directory", "store", "not an absolute path"},
	}
	for _, reader := range readers {
		for _, root := range roots {
			t.Run(reader.name+"/"+root.name, func(t *testing.T) {
				cwd := t.TempDir()
				t.Chdir(cwd)
				plantAmbientStore(t, cwd)
				// The relative root's own directory, planted too: "store" is
				// as ambient as "." when it sits in somebody's project.
				ambientStore := filepath.Join(cwd, "store")
				if err := os.MkdirAll(ambientStore, 0o755); err != nil {
					t.Fatal(err)
				}
				plantAmbientStore(t, ambientStore)

				n, err := reader.read(&Manager{Root: root.root, Stderr: io.Discard})
				if err == nil || !strings.Contains(err.Error(), root.wantErr) {
					t.Fatalf("%s error = %v, want %q", reader.name, err, root.wantErr)
				}
				if n != 0 {
					t.Errorf("%s returned %d entries from the working directory", reader.name, n)
				}
			})
		}
	}
}
