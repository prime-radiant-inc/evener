package plugins

import (
	"context"
	"testing"
)

func TestEnabledPluginDirs_FiltersDisabledAndBroken(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t) // installs "widget" (relative source) — from install_test.go
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// enabled → included
	dirs := m.EnabledPluginDirs(nil)
	if len(dirs) != 1 || dirs[0] != entry.InstallPath {
		t.Fatalf("EnabledPluginDirs = %v, want [%s]", dirs, entry.InstallPath)
	}

	// disabled → excluded
	if err := m.SetEnabled("widget", name, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if dirs := m.EnabledPluginDirs(nil); len(dirs) != 0 {
		t.Fatalf("disabled plugin still returned: %v", dirs)
	}
}

func TestEnabledPluginDirs_ExplicitFirstAndDeduped(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	// An explicit --plugin-dir pointing at a DIFFERENT dir with the SAME plugin name
	// ("widget") must win: the registry dir is deduped out.
	explicitDir := t.TempDir()
	writePlugin(t, explicitDir, "widget", nil) // from validate_test.go

	dirs := m.EnabledPluginDirs([]string{explicitDir})
	if len(dirs) != 1 || dirs[0] != explicitDir {
		t.Fatalf("EnabledPluginDirs=%v, want [%s] (explicit wins over registry %s)", dirs, explicitDir, entry.InstallPath)
	}
}
