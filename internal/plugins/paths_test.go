package plugins

import (
	"errors"
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

// A name that is a legal path component can still be one the store's own
// machinery has already spoken for: the two scratch directories a marketplace
// fetch renames through, and the '@' that separates plugin from marketplace in
// a registry key. Both are a marketplace's rule alone — the scratch
// directories are the marketplaces directory's own children, and a registry
// key is parsed at its LAST '@', which only a marketplace name carrying one
// moves.
func TestValidNameComponent_RefusesTheScratchNamesAndAtForMarketplaces(t *testing.T) {
	refused := []struct{ kind, name, rule string }{
		{"marketplace", stagingCloneName, "scratch"},
		{"marketplace", asideCloneName, "scratch"},
		{"marketplace", "foo@bar", "'@'"},
	}
	for _, tc := range refused {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			err := validNameComponent(tc.kind, tc.name)
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("validNameComponent(%q, %q) = %v, want ErrInvalidName", tc.kind, tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("error = %v, want it to name the rule (%q)", err, tc.rule)
			}
			if !strings.Contains(err.Error(), tc.kind) {
				t.Fatalf("error = %v, want it to name the kind (%q)", err, tc.kind)
			}
		})
	}
	// A plugin named for one of them collides with nothing: its cache
	// directory is cache/<marketplace>/<plugin>, a sibling of no scratch
	// directory, and the staging an install uses is one level deeper still.
	// Nor does a plugin's '@' collide: it sits before the key's last one, so
	// foo@bar in acme keys foo@bar@acme and reads back as the name the
	// catalog gave.
	for _, name := range []string{stagingCloneName, asideCloneName, "foo@bar"} {
		if err := validNameComponent("plugin", name); err != nil {
			t.Errorf("validNameComponent(\"plugin\", %q) = %v, want nil", name, err)
		}
	}
	// Neither rule reaches a name that merely starts with a dot or holds one.
	for _, name := range []string{"acme", "acme-corp", ".hidden", "widget.v2"} {
		if err := validNameComponent("marketplace", name); err != nil {
			t.Errorf("validNameComponent(%q) = %v, want nil", name, err)
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
