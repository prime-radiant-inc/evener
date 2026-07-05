package main

// Tests for serf/command/list (design §10 / P3): the hub RPC handler that
// flattens loaded plugins' slash commands into a catalog for autocomplete
// display.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/plugins"
)

func writeCommandListTestPlugin(t *testing.T, dir, pluginName string) {
	t.Helper()
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(`{"name": "`+pluginName+`"}`), 0644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "greet.md"),
		[]byte("---\ndescription: Greets someone\nargument-hint: \"[name]\"\n---\nHi $ARGUMENTS"), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

func TestHubCommandList_ReturnsLoadedPluginCommands(t *testing.T) {
	pluginDir := t.TempDir()
	writeCommandListTestPlugin(t, pluginDir, "greeter")

	resp, err := hubCommandList(hubcore.WebConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("Commands = %v, want 1 entry", resp.Commands)
	}
	got := resp.Commands[0]
	if got.Name != "greet" {
		t.Errorf("Name = %q, want %q", got.Name, "greet")
	}
	if got.PluginName != "greeter" {
		t.Errorf("PluginName = %q, want %q", got.PluginName, "greeter")
	}
	if got.Description != "Greets someone" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.ArgumentHint != "[name]" {
		t.Errorf("ArgumentHint = %q, want %q", got.ArgumentHint, "[name]")
	}
}

func TestHubCommandList_NoPluginDirsReturnsEmpty(t *testing.T) {
	// With no explicit PluginDirs, hubCommandList falls back to
	// plugins.Manager.EnabledPluginDirs, which reads the installed-plugin
	// registry under the XDG default plugins root. Point it at an empty temp
	// dir so the result is deterministically empty regardless of what the
	// machine running this test actually has installed under
	// ~/.config/serf/plugins.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	resp, err := hubCommandList(hubcore.WebConfig{})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Fatalf("Commands = %v, want empty", resp.Commands)
	}
}

// TestHubCommandList_IncludesRegistryEnabledPlugin proves hubCommandList
// reflects what a real session actually loads (EnabledPluginDirs), not just
// the display-only glob of the plugin store's immediate subdirectories
// (pluginDirsFromConfig). Before this fix, a plugin installed and enabled
// through the marketplace/registry system (living at
// cache/<marketplace>/<plugin>/<sha>, not a direct child of the plugins
// root) could never appear in serf/command/list, even though a spawned
// session would load its commands just fine.
func TestHubCommandList_IncludesRegistryEnabledPlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pluginRoot := t.TempDir()
	mgr := plugins.NewManager(pluginRoot)

	mktDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mktDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktDir, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"greeter","source":"./plugins/greeter"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCommandListTestPlugin(t, filepath.Join(mktDir, "plugins", "greeter"), "greeter")

	ctx := context.Background()
	if _, err := mgr.AddMarketplace(ctx, "acme", plugins.Source{Kind: plugins.SourceDirectory, Path: mktDir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := mgr.Install(ctx, "greeter", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	resp, err := hubCommandList(hubcore.WebConfig{PluginRoot: pluginRoot})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 1 || resp.Commands[0].Name != "greet" {
		t.Fatalf("Commands = %+v, want the registry-installed plugin's %q command", resp.Commands, "greet")
	}
}

func TestHubCommandList_MultiplePluginsSortedByName(t *testing.T) {
	dirA := t.TempDir()
	writeCommandListTestPlugin(t, dirA, "plugin-a")
	dirB := filepath.Join(t.TempDir(), "b")
	if err := os.MkdirAll(dirB, 0755); err != nil {
		t.Fatal(err)
	}
	metaDir := filepath.Join(dirB, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(`{"name": "plugin-b"}`), 0644); err != nil {
		t.Fatal(err)
	}
	commandsDir := filepath.Join(dirB, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "aardvark.md"), []byte("---\ndescription: first alphabetically\n---\nBody"), 0644); err != nil {
		t.Fatal(err)
	}

	resp, err := hubCommandList(hubcore.WebConfig{PluginDirs: []string{dirA, dirB}})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 2 {
		t.Fatalf("Commands = %v, want 2 entries", resp.Commands)
	}
	if resp.Commands[0].Name != "aardvark" || resp.Commands[1].Name != "greet" {
		t.Errorf("Commands not sorted by name: %+v", resp.Commands)
	}
}

// TestHubCommandList_BrokenPluginDirDoesNotBrickCatalog proves hubCommandList
// is fail-soft (finding #4): a broken/mid-edit plugin dir alongside a healthy
// one must not abort the whole catalog with an error. This mirrors the
// fail-soft loading session init already does for the exact same reason
// (loadPluginsFailSoft in agent/session_init.go) — command/list must not
// reintroduce the fragility that fix closed.
func TestHubCommandList_BrokenPluginDirDoesNotBrickCatalog(t *testing.T) {
	brokenDir := t.TempDir() // no .claude-plugin/plugin.json at all: Load fails.
	healthyDir := t.TempDir()
	writeCommandListTestPlugin(t, healthyDir, "greeter")

	resp, err := hubCommandList(hubcore.WebConfig{PluginDirs: []string{brokenDir, healthyDir}})
	if err != nil {
		t.Fatalf("hubCommandList: %v, want the broken dir skipped rather than aborting the whole catalog", err)
	}
	if len(resp.Commands) != 1 || resp.Commands[0].Name != "greet" {
		t.Fatalf("Commands = %+v, want the healthy plugin's %q command despite the broken dir", resp.Commands, "greet")
	}
}

func TestHubCommandList_ViaTypedRPCClient(t *testing.T) {
	pluginDir := t.TempDir()
	writeCommandListTestPlugin(t, pluginDir, "greeter")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:       hubcore.NewPastIndex(""),
		PluginDirs: []string{pluginDir},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.CommandList(context.Background())
	if err != nil {
		t.Fatalf("CommandList: %v", err)
	}
	if len(resp.Commands) != 1 || resp.Commands[0].Name != "greet" {
		t.Fatalf("Commands = %+v, want a single %q entry", resp.Commands, "greet")
	}
}
