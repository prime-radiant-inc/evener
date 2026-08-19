package plugins

import (
	"path/filepath"
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

func TestLegacyRootFor(t *testing.T) {
	cases := []struct {
		name string
		root string
		want string
	}{
		{
			name: "default-shaped root swaps evener for serf",
			root: "/x/y/evener/plugins",
			want: "/x/y/serf/plugins",
		},
		{
			name: "custom root with no evener segment has no legacy equivalent",
			root: "/store",
			want: "",
		},
		{
			name: "root whose parent isn't evener has no legacy equivalent",
			root: "/x/y/notevener/plugins",
			want: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacyRootFor(tt.root); got != tt.want {
				t.Errorf("legacyRootFor(%q) = %q, want %q", tt.root, got, tt.want)
			}
		})
	}
}

func TestManagerPaths(t *testing.T) {
	m := NewManager("/store")
	cases := map[string]string{
		m.registryPath():                         "/store/installed_plugins.json",
		m.marketplacesDir():                      "/store/marketplaces",
		m.cacheDir():                             "/store/cache",
		m.lockPath():                             "/store/.lock",
		m.marketplaceDir("acme"):                 "/store/marketplaces/acme",
		m.pluginCacheDir("acme", "widget", "ab"): "/store/cache/acme/widget/ab",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
