package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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
	dirs := m.EnabledPluginDirs(context.Background(), nil)
	if len(dirs) != 1 || dirs[0] != entry.InstallPath {
		t.Fatalf("EnabledPluginDirs = %v, want [%s]", dirs, entry.InstallPath)
	}

	// disabled → excluded
	if err := m.SetEnabled(context.Background(), "widget", name, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if dirs := m.EnabledPluginDirs(context.Background(), nil); len(dirs) != 0 {
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

	dirs := m.EnabledPluginDirs(context.Background(), []string{explicitDir})
	if len(dirs) != 1 || dirs[0] != explicitDir {
		t.Fatalf("EnabledPluginDirs=%v, want [%s] (explicit wins over registry %s)", dirs, explicitDir, entry.InstallPath)
	}
}

func TestEnabledPluginDirs_WarnsOnDuplicateExplicit(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writePlugin(t, a, "dup", nil)
	writePlugin(t, b, "dup", nil) // same Manifest.Name in two explicit dirs

	m := NewManager(t.TempDir())
	var warn bytes.Buffer
	m.Stderr = &warn

	dirs := m.EnabledPluginDirs(context.Background(), []string{a, b})
	if len(dirs) != 1 || dirs[0] != a {
		t.Fatalf("EnabledPluginDirs=%v, want [%s] (first wins)", dirs, a)
	}
	if !strings.Contains(warn.String(), "duplicate plugin name") {
		t.Fatalf("expected a duplicate warning, got: %q", warn.String())
	}
}

func TestEnabledPluginDirs_RendersResolverDiagnostics(t *testing.T) {
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid")
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(filepath.Join(root, "store"))
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
		"broken@market": {{InstallPath: broken, Enabled: true, Source: Source{Kind: SourceDirectory, Path: broken}}},
	}}); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	m.Stderr = &warn

	if dirs := m.EnabledPluginDirs(context.Background(), []string{invalid}); len(dirs) != 0 {
		t.Fatalf("EnabledPluginDirs = %v, want no valid directories", dirs)
	}
	got := warn.String()
	if !strings.Contains(got, "warning: skipping invalid --plugin-dir "+invalid) {
		t.Fatalf("missing explicit warning: %q", got)
	}
	if !strings.Contains(got, "warning: skipping broken plugin "+broken) {
		t.Fatalf("missing registry warning: %q", got)
	}
}

// An unresolved store root stops the resolver before the registry, so the
// warning has to name the store rather than blame a --plugin-dir the caller
// passed. Explicit directories are the caller's own and still resolve.
func TestEnabledPluginDirs_WarnsOnUnresolvedStoreRoot(t *testing.T) {
	explicit := t.TempDir()
	writePlugin(t, explicit, "widget", nil)
	m := NewManager(t.TempDir())
	m.Root = ""
	var warn bytes.Buffer
	m.Stderr = &warn

	dirs := m.EnabledPluginDirs(context.Background(), []string{explicit})
	if len(dirs) != 1 || dirs[0] != explicit {
		t.Fatalf("EnabledPluginDirs = %v, want [%s]", dirs, explicit)
	}
	got := warn.String()
	if !strings.Contains(got, "no plugin store root is configured") {
		t.Fatalf("missing store-root warning: %q", got)
	}
	if strings.Contains(got, "--plugin-dir") {
		t.Fatalf("store-root warning blamed a --plugin-dir: %q", got)
	}
}
