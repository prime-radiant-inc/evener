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
