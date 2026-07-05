package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestList_FlagsBroken(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	items, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Plugin != "widget" || items[0].Broken {
		t.Fatalf("List = %+v, want one healthy widget", items)
	}

	// Corrupt the installed plugin on disk → List must flag it broken.
	os.RemoveAll(entry.InstallPath)
	items, _ = m.List()
	if !items[0].Broken {
		t.Fatal("List did not flag a missing install dir as broken")
	}
}

func TestUpdateAll_UpgradesGitBackedSkipsRelative(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "gitwidget", nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[` +
		`{"name":"gitwidget","source":{"source":"url","url":"` + pluginRepo + `"}},` +
		`{"name":"relwidget","source":"./plugins/relwidget"}]}`
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(mktRepo, "plugins", "relwidget"), "relwidget", nil)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "gitwidget", "acme"); err != nil {
		t.Fatalf("Install gitwidget: %v", err)
	}
	if _, err := m.Install(context.Background(), "relwidget", "acme"); err != nil {
		t.Fatalf("Install relwidget: %v", err)
	}

	// advance the git plugin HEAD
	if err := os.WriteFile(filepath.Join(pluginRepo, "extra.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", pluginRepo, "commit", "-aqm", "v2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	updated, err := m.UpdateAll(context.Background())
	if err != nil {
		t.Fatalf("UpdateAll: %v", err)
	}
	// only the git-backed plugin upgrades; the relative one is skipped.
	if len(updated) != 1 {
		t.Fatalf("UpdateAll upgraded %d plugins, want 1 (git-backed only): %+v", len(updated), updated)
	}
}

func TestUpdateAll_SkipsEmptyEntry(t *testing.T) {
	m := NewManager(t.TempDir())
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{"ghost@mkt": {}}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateAll(context.Background()); err != nil {
		t.Fatalf("UpdateAll on empty entry should be a no-op, got: %v", err)
	}
}
