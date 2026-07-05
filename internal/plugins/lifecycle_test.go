package plugins

import (
	"context"
	"os"
	"path/filepath"
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

func TestRemove_DirectorySourceKeepsContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"local","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(dir, "plugins", "widget"), "widget", nil)

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", "local")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	// The plugin must be referenced in place (under the marketplace dir, not the cache).
	if !strings.HasPrefix(entry.InstallPath, dir) {
		t.Fatalf("expected in-place install under %s, got %s", dir, entry.InstallPath)
	}
	if err := m.Remove("widget", "local"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Remove must NOT delete the directory-source plugin's own files.
	if _, err := os.Stat(filepath.Join(dir, "plugins", "widget", ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("Remove deleted the in-place directory-source plugin: %v", err)
	}
}
