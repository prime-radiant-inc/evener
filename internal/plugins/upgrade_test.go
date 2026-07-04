package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpgrade_NewShaDirOldRemains(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// A marketplace whose plugin source is a github/url git repo (so it has a sha).
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"}}]}`), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin repo HEAD.
	os.WriteFile(filepath.Join(pluginRepo, "extra.txt"), []byte("v2"), 0o644)
	cmd := exec.Command("git", "-C", pluginRepo, "commit", "-aqm", "v2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Run()

	second, err := m.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if second.InstallPath == first.InstallPath {
		t.Fatal("upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(first.InstallPath); err != nil {
		t.Fatal("old sha-dir was deleted; upgrade must not GC")
	}
	reg, _ := LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].InstallPath != second.InstallPath {
		t.Fatal("registry not repointed to new dir")
	}
}
