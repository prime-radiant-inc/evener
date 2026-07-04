package plugins

import (
	"path/filepath"
	"testing"
)

func TestDefaultRoot_UsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	got := DefaultRoot()
	want := filepath.Join("/tmp/xdgcfg", "serf", "plugins")
	if got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestManagerPaths(t *testing.T) {
	m := NewManager("/store")
	cases := map[string]string{
		m.registryPath():                       "/store/installed_plugins.json",
		m.marketplacesDir():                    "/store/marketplaces",
		m.cacheDir():                           "/store/cache",
		m.lockPath():                           "/store/.lock",
		m.marketplaceDir("acme"):               "/store/marketplaces/acme",
		m.pluginCacheDir("acme", "widget", "ab"): "/store/cache/acme/widget/ab",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
