package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	agentplugin "primeradiant.com/serf/agent/plugin"
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
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

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

func TestUpgrade_NoOpKeepsLiveDir(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	canary := filepath.Join(first.InstallPath, "CANARY")
	if err := os.WriteFile(canary, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := m.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Upgrade no-op: %v", err)
	}
	if after.InstallPath != first.InstallPath {
		t.Fatalf("no-op upgrade moved dir: %s -> %s", first.InstallPath, after.InstallPath)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("no-op upgrade destroyed the live dir (canary gone): %v", err)
	}
}

func TestUpgrade_ManifestLessPlugin_FallbackAppliedToNewShaDir(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-mcp",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"bare": {"command":"echo"}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin repo HEAD so Upgrade materializes a NEW sha-dir.
	os.WriteFile(filepath.Join(pluginRepo, "extra.txt"), []byte("v2"), 0o644)
	cmd := exec.Command("git", "-C", pluginRepo, "commit", "-aqm", "v2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	second, err := m.Upgrade(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if second.InstallPath == first.InstallPath {
		t.Fatal("upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(filepath.Join(second.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing from the new sha-dir: %v", err)
	}
	inst, err := agentplugin.Load(second.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 1 {
		t.Fatalf("MCPConfigs = %+v, want one entry after upgrade", inst.MCPConfigs)
	}
}
