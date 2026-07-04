package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeMarketplaceRepo builds a git repo containing a .claude-plugin/marketplace.json
// naming one plugin, and returns its path.
func makeMarketplaceRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[` +
		`{"name":"widget","description":"a widget","source":"./plugins/widget"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, dir, "README.md", "mkt") // also commits marketplace.json via `git add .`
	return dir
}

func TestAddListRemoveMarketplace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())

	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if ref.InstallLocation == "" {
		t.Fatal("empty InstallLocation")
	}

	list, err := m.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := list["acme"]; !ok {
		t.Fatalf("marketplace 'acme' not listed: %v", list)
	}

	if err := m.RemoveMarketplace("acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	list, _ = m.ListMarketplaces()
	if _, ok := list["acme"]; ok {
		t.Fatal("marketplace still present after remove")
	}
	if _, err := os.Stat(m.marketplaceDir("acme")); !os.IsNotExist(err) {
		t.Fatal("clone dir not deleted after remove")
	}
}

func TestAddMarketplace_GitSubdirBrowse(t *testing.T) {
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
	makeGitRepo(t, repo, "README.md", "root") // commits everything incl. mkt/

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceGitSubdir, URL: repo, Path: "mkt"}); err != nil {
		t.Fatalf("AddMarketplace git-subdir: %v", err)
	}
	cat, err := m.Browse("acme")
	if err != nil {
		t.Fatalf("Browse git-subdir marketplace: %v", err)
	}
	if cat.Name != "acme" || len(cat.Plugins) != 1 {
		t.Fatalf("catalog = %+v", cat)
	}
}

func TestRemoveMarketplace_DirectorySourceKeepsContents(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := makeMarketplaceRepo(t, "local")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace directory: %v", err)
	}
	if err := m.RemoveMarketplace("local"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("directory source contents deleted on remove: %v", err)
	}
}
