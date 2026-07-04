package plugins

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRemoveEnableDisable(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	if err := m.SetEnabled("widget", name, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	reg, _ := LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].Enabled {
		t.Fatal("entry still enabled after disable")
	}

	if err := m.SetAutoUpgrade("widget", name, true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	reg, _ = LoadRegistry(m.registryPath())
	if !reg.Plugins["widget@acme"][0].AutoUpgrade {
		t.Fatal("autoUpgrade not set")
	}

	if err := m.Remove("widget", name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	reg, _ = LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["widget@acme"]; ok {
		t.Fatal("entry still present after remove")
	}
	// cache dir gone (installPath was under the cache root)
	if strings.HasPrefix(entry.InstallPath, m.cacheDir()) {
		if _, err := os.Stat(entry.InstallPath); !os.IsNotExist(err) {
			t.Fatal("cache dir not deleted on remove")
		}
	}
}
