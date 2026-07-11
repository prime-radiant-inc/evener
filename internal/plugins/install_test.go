package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentplugin "primeradiant.com/serf/agent/plugin"
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

// TestInstall_LazyFetchesSeededPointer guards against a self-deadlock: Install
// already holds m.lockPath() when it reaches catalogPlugin -> ensureFetched, so
// ensureFetched must NOT try to acquire that same lock itself (flock(2) is
// per-open-file-description, not per-process/reentrant — a second acquisition
// attempt from the same process spins until its own timeout and fails).
func TestInstall_LazyFetchesSeededPointer(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	// seed a pointer (empty InstallLocation) directly, as SeedDefaultMarketplaces would.
	if err := m.saveMarketplaces(Marketplaces{name: {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}

	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install on seeded-but-unfetched marketplace: %v", err)
	}
	if !entry.Enabled {
		t.Error("installed entry not enabled")
	}
	mk, _ := m.ListMarketplaces()
	if mk[name].InstallLocation == "" {
		t.Fatal("InstallLocation not backfilled after lazy fetch via Install")
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

func TestInstall_ManifestLessPlugin_MCPServerRegisters(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// A plugin repo with NO .claude-plugin (or .codex-plugin) manifest at all
	// — the private-journal-mcp shape: a bare package, no plugin.json.
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "README.md", "bare mcp server, no manifest")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-mcp",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"bare": {"command":"echo","args":["hi"]}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Install of a manifest-less plugin with an mcpServers entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	inst, err := agentplugin.Load(entry.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_bare-mcp_bare" {
		t.Fatalf("MCPConfigs = %+v, want one entry named plugin_bare-mcp_bare", inst.MCPConfigs)
	}
}

// TestInstall_ManifestLessPlugin_NoUsableFields_Installs mirrors
// private-journal-mcp@superpowers-marketplace: a bare repo with no plugin
// scaffolding whose marketplace entry declares no components either. Claude
// Code installs that shape (the plugin contributes only what conventional
// dirs auto-discover — here nothing), so serf must install it too,
// synthesizing a minimal name/description manifest.
func TestInstall_ManifestLessPlugin_NoUsableFields_Installs(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "README.md", "bare, undeclared")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-nothing",
	  "source":{"source":"url","url":"` + pluginRepo + `"}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "bare-nothing", "acme")
	if err != nil {
		t.Fatalf("Install of a manifest-less, component-less plugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	inst, err := agentplugin.Load(entry.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if inst.Manifest.Name != "bare-nothing" {
		t.Errorf("Manifest.Name = %q, want bare-nothing", inst.Manifest.Name)
	}
	if len(inst.MCPConfigs) != 0 || len(inst.Commands) != 0 || len(inst.Agents) != 0 || len(inst.Skills) != 0 {
		t.Errorf("expected zero components, got %+v", inst)
	}
	reg, _ := LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["bare-nothing@acme"]; !ok {
		t.Fatal("a successful install must leave a registry entry")
	}
}

// TestInstall_PluginWithOwnManifest_EntryIgnored is the required regression:
// a plugin that ships its own plugin.json is completely unchanged by Part 2,
// even when its marketplace entry ALSO declares components.
func TestInstall_PluginWithOwnManifest_EntryIgnored(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil) // has its own plugin.json, no mcpServers
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"widget",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"should-be-ignored": {"command":"echo"}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	inst, err := agentplugin.Load(entry.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 0 {
		t.Fatalf("MCPConfigs = %+v, want none — entry's mcpServers must be ignored when plugin.json exists", inst.MCPConfigs)
	}
}
