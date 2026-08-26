package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func writePreviewFixturePlugin(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"preview-fixture","version":"1.2.3","description":"preview only","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]},"mcpServers":{"marker":{"command":"touch","args":["` + marker + `"]}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginPreviewIsSideEffectFree(t *testing.T) {
	pluginDir := t.TempDir()
	launchRoot := t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker")
	writePreviewFixturePlugin(t, pluginDir, marker)
	cwd := t.TempDir()
	ctl := newHubPluginsController(t.TempDir(), launchRoot)
	selected := []string{"preview-fixture"}
	resp, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{
		CWD: cwd,
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{pluginDir},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(resp.Plugins) != 1 || !resp.Plugins[0].Selected || resp.Plugins[0].HookCount != 1 || resp.Plugins[0].MCPCount != 1 {
		t.Fatalf("Preview plugins = %+v", resp.Plugins)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview executed marker-producing hook: stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(launchRoot, "projects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created session metadata: stat=%v", err)
	}
}

func TestPluginSelectionBeforeSpawn(t *testing.T) {
	pluginDir := t.TempDir()
	writePreviewFixturePlugin(t, pluginDir, filepath.Join(t.TempDir(), "marker"))
	launchRoot := t.TempDir()
	pluginRoot := t.TempDir()
	spawner := &recordingSpawner{}
	cfg := hubcore.WebConfig{LaunchConfigRoot: launchRoot, PluginRoot: pluginRoot, Spawner: spawner}
	selected := []string{"preview-fixture"}
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatal(err)
	}
	_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   "/tmp",
		Model: "openai/gpt-5",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{pluginDir},
			EnabledPlugins: &selected,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable") {
		t.Fatalf("ThreadStart error = %v, want invalid selection", err)
	}
	if got := len(spawner.Spawns()); got != 0 {
		t.Fatalf("spawn calls = %d, want 0", got)
	}
}
