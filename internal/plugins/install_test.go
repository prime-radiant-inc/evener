package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeInstallableMarketplace builds a git marketplace whose one plugin's source
// is a bare-string "./plugins/widget" living in the same repo.
func makeInstallableMarketplace(t *testing.T) (mktRepo, name string) {
	t.Helper()
	name = "acme"
	dir := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644)
	writePlugin(t, filepath.Join(dir, "plugins", "widget"), "widget", nil)
	makeGitRepo(t, dir, "README.md", "x")
	return dir, name
}

func TestInstall_MaterializesAndRegisters(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !entry.Enabled {
		t.Error("installed entry not enabled")
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("materialized plugin.json missing: %v", err)
	}

	reg, _ := LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["widget@acme"]; !ok {
		t.Fatalf("registry missing widget@acme: %+v", reg.Plugins)
	}
}

func TestInstall_FromGitSubdirMarketplace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, "mkt", ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mkt", ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(repo, "mkt", "plugins", "widget"), "widget", nil)
	makeGitRepo(t, repo, "README.md", "root")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceGitSubdir, URL: repo, Path: "mkt"}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install from git-subdir marketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("materialized plugin.json missing: %v", err)
	}
}
