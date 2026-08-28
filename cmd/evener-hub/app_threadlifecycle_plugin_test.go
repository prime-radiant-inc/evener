package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/plugins"
)

func writePreviewFixturePlugin(t *testing.T, dir, name, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.2.3","description":"preview only","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]},"mcpServers":{"marker":{"command":"touch","args":["` + marker + `"]}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, componentDir := range []string{filepath.Join(dir, "commands"), filepath.Join(dir, "agents"), filepath.Join(dir, "skills", "one")} {
		if err := os.MkdirAll(componentDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "commands", "hello.md"), []byte("hello command"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: review\n---\nReview"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "one", "SKILL.md"), []byte("---\nname: one\ndescription: skill\n---\nSkill"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginPreviewIsSideEffectFree(t *testing.T) {
	pluginDir := t.TempDir()
	launchRoot := t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker")
	writePreviewFixturePlugin(t, pluginDir, "preview-fixture", marker)
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
	if len(resp.Plugins) != 1 || !resp.Plugins[0].Selected || resp.Plugins[0].SkillCount != 1 || resp.Plugins[0].AgentCount != 1 || resp.Plugins[0].CommandCount != 1 || resp.Plugins[0].HookCount != 1 || resp.Plugins[0].MCPCount != 1 {
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
	writePreviewFixturePlugin(t, pluginDir, "preview-fixture", filepath.Join(t.TempDir(), "marker"))
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

func TestThreadStartPreservesExplicitPluginDirsInChildArgs(t *testing.T) {
	launchRoot := t.TempDir()
	pluginRoot := t.TempDir()
	explicitDir := filepath.Join(t.TempDir(), "explicit")
	installedDir := filepath.Join(pluginRoot, "installed-alpha")
	writePreviewFixturePlugin(t, explicitDir, "explicit-local", filepath.Join(t.TempDir(), "explicit-marker"))
	writePreviewFixturePlugin(t, installedDir, "alpha", filepath.Join(t.TempDir(), "installed-marker"))
	registry := plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"alpha@acme": {{
				InstallPath: installedDir,
				Version:     "1.0.0",
				InstalledAt: time.Unix(1, 0),
				LastUpdated: time.Unix(1, 0),
				Enabled:     true,
				AutoUpgrade: false,
				Source:      plugins.Source{Kind: plugins.SourceDirectory, Path: installedDir},
			}},
		},
	}
	if err := plugins.SaveRegistry(filepath.Join(pluginRoot, "installed_plugins.json"), registry); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	spawner := &recordingSpawner{}
	cfg := hubcore.WebConfig{LaunchConfigRoot: launchRoot, PluginRoot: pluginRoot, Spawner: spawner}
	selected := []string{"alpha"}

	_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{explicitDir},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}
	got := spawns[0].Resolved.Effective
	if !reflect.DeepEqual(got.PluginDirs, []string{explicitDir}) {
		t.Fatalf("spawn PluginDirs = %v, want explicit dirs only [%q]", got.PluginDirs, explicitDir)
	}
	if got.EnabledPlugins == nil || !reflect.DeepEqual(*got.EnabledPlugins, []string{"alpha"}) {
		t.Fatalf("spawn EnabledPlugins = %#v, want [alpha]", got.EnabledPlugins)
	}
	args := launchconfig.ToArgs(spawns[0].Resolved)
	if !slicesContainOrderedFlag(args, "--plugin-dir", explicitDir) {
		t.Fatalf("spawn args = %v, want explicit plugin dir %q", args, explicitDir)
	}
	if slicesContainOrderedFlag(args, "--plugin-dir", installedDir) {
		t.Fatalf("spawn args = %v, do not want registry-installed dir %q", args, installedDir)
	}
	if !slices.Contains(args, "--enabled-plugins=alpha") {
		t.Fatalf("spawn args = %v, want enabled plugin selection", args)
	}
}

func TestThreadStartPassesPluginRootToChildWithoutLeakingRegistryDirs(t *testing.T) {
	launchRoot := t.TempDir()
	pluginRoot := t.TempDir()
	explicitDir := filepath.Join(t.TempDir(), "explicit-alpha")
	installedDir := filepath.Join(pluginRoot, "installed-alpha")
	writePreviewFixturePlugin(t, explicitDir, "alpha", filepath.Join(t.TempDir(), "explicit-marker"))
	writePreviewFixturePlugin(t, installedDir, "alpha", filepath.Join(t.TempDir(), "installed-marker"))
	registry := plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"alpha@acme": {{
				InstallPath: installedDir,
				Version:     "1.0.0",
				InstalledAt: time.Unix(1, 0),
				LastUpdated: time.Unix(1, 0),
				Enabled:     true,
				AutoUpgrade: false,
				Source:      plugins.Source{Kind: plugins.SourceDirectory, Path: installedDir},
			}},
		},
	}
	if err := plugins.SaveRegistry(filepath.Join(pluginRoot, "installed_plugins.json"), registry); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	spawner := &recordingSpawner{}
	cfg := hubcore.WebConfig{LaunchConfigRoot: launchRoot, PluginRoot: pluginRoot, Spawner: spawner}
	selected := []string{"alpha"}

	_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{explicitDir},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}
	if spawns[0].PluginRoot != pluginRoot {
		t.Fatalf("spawn PluginRoot = %q, want %q", spawns[0].PluginRoot, pluginRoot)
	}
	if !reflect.DeepEqual(spawns[0].Resolved.Effective.PluginDirs, []string{explicitDir}) {
		t.Fatalf("spawn PluginDirs = %v, want explicit dirs only [%q]", spawns[0].Resolved.Effective.PluginDirs, explicitDir)
	}
	args := buildSpawnArgs(spawns[0])
	if !slicesContainOrderedFlag(args, "--plugin-root", pluginRoot) {
		t.Fatalf("spawn args = %v, want plugin root %q", args, pluginRoot)
	}
	if !slicesContainOrderedFlag(args, "--plugin-dir", explicitDir) {
		t.Fatalf("spawn args = %v, want explicit plugin dir %q", args, explicitDir)
	}
	if slicesContainOrderedFlag(args, "--plugin-dir", installedDir) {
		t.Fatalf("spawn args = %v, do not want registry-installed dir %q", args, installedDir)
	}
}

func slicesContainOrderedFlag(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
