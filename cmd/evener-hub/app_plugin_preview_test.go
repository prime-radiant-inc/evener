package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/plugins"
)

func TestPluginPreviewControllerMapsCandidates(t *testing.T) {
	ctl := newHubPluginsController(t.TempDir(), t.TempDir())
	if _, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: "relative"}); err == nil {
		t.Fatal("Preview accepted invalid cwd")
	}
	got, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Plugins == nil || got.Diagnostics == nil || got.SelectionErrors == nil {
		t.Fatalf("preview slices must be non-nil: %+v", got)
	}
}

// Before a launch directory is chosen there is no project context, but the
// user-level inventory (global layer) already exists — the spawn pane previews
// with an empty cwd on mount and must not fail for it.
func TestPluginPreviewEmptyCWDResolvesUserLayers(t *testing.T) {
	firstDir := t.TempDir()
	writePreviewFixturePlugin(t, firstDir, "global-fixture", filepath.Join(t.TempDir(), "marker"))
	secondDir := t.TempDir()
	writePreviewFixturePlugin(t, secondDir, "other-fixture", filepath.Join(t.TempDir(), "marker"))
	launchRoot := t.TempDir()
	globalLayer := "plugin_dirs = [\"" + firstDir + "\", \"" + secondDir + "\"]\n"
	if err := os.WriteFile(filepath.Join(launchRoot, "launch.toml"), []byte(globalLayer), 0o644); err != nil {
		t.Fatalf("write global layer: %v", err)
	}
	ctl := newHubPluginsController(t.TempDir(), launchRoot)

	// With no per-launch selection every loadable candidate is selected — the
	// same default a picked directory with no overrides would give.
	for _, cwd := range []string{"", "   "} {
		preview, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: cwd})
		if err != nil {
			t.Fatalf("Preview for cwd %q: %v", cwd, err)
		}
		if len(preview.Plugins) != 2 {
			t.Fatalf("Preview for cwd %q plugins = %+v, want both global directory candidates", cwd, preview.Plugins)
		}
		for _, candidate := range preview.Plugins {
			if !candidate.Selected {
				t.Fatalf("Preview for cwd %q candidate not selected by default: %+v", cwd, candidate)
			}
		}
	}

	// Per-launch overrides merge without a directory, too.
	override := []string{"other-fixture"}
	preview, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{
		CWD:             "",
		LaunchOverrides: &appwire.LaunchConfigLayer{EnabledPlugins: &override},
	})
	if err != nil {
		t.Fatalf("Preview with overrides for empty cwd: %v", err)
	}
	byName := make(map[string]appwire.PluginLaunchCandidate, len(preview.Plugins))
	for _, candidate := range preview.Plugins {
		byName[candidate.Name] = candidate
	}
	if byName["global-fixture"].Selected || !byName["other-fixture"].Selected {
		t.Fatalf("Preview with overrides selection = %+v, want only other-fixture selected", preview.Plugins)
	}
}

func TestPluginPreviewMissingCWDMatchesStartAfterCreation(t *testing.T) {
	pluginDir := t.TempDir()
	writePreviewFixturePlugin(t, pluginDir, "preview-fixture", filepath.Join(t.TempDir(), "marker"))
	launchRoot := t.TempDir()
	pluginRoot := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "new-session")
	selected := []string{"preview-fixture"}
	overrides := &appwire.LaunchConfigLayer{
		PluginDirs:     []string{pluginDir},
		EnabledPlugins: &selected,
	}
	ctl := newHubPluginsController(pluginRoot, launchRoot)

	preview, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: cwd, LaunchOverrides: overrides})
	if err != nil {
		t.Fatalf("Preview for missing cwd: %v", err)
	}
	if len(preview.Plugins) != 1 || preview.Plugins[0].Name != "preview-fixture" || !preview.Plugins[0].Selected {
		t.Fatalf("Preview plugins = %+v, want selected preview-fixture", preview.Plugins)
	}
	if _, err := os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Preview created cwd: stat=%v", err)
	}

	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatalf("create cwd: %v", err)
	}
	spawner := &recordingSpawner{}
	_, err = hubThreadStart(context.Background(), hubcore.WebConfig{
		LaunchConfigRoot: launchRoot,
		PluginRoot:       pluginRoot,
		Spawner:          spawner,
	}, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:             cwd,
		Model:           "openai/gpt-5",
		LaunchOverrides: overrides,
	})
	if err != nil {
		t.Fatalf("ThreadStart after creating cwd: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}
	if got := spawns[0].Resolved.Effective.EnabledPlugins; got == nil || len(*got) != 1 || (*got)[0] != "preview-fixture" {
		t.Fatalf("started enabled plugins = %#v, want [preview-fixture]", got)
	}
}

func TestPluginPreviewMissingNonGitChildMatchesStartAfterCreation(t *testing.T) {
	projectRoot := t.TempDir()
	missingCWD := filepath.Join(projectRoot, "new-session")
	projectPluginDir := filepath.Join(t.TempDir(), "project-plugin")
	writePreviewFixturePlugin(t, projectPluginDir, "project-fixture", filepath.Join(t.TempDir(), "project-marker"))
	ancestorPluginDir := filepath.Join(t.TempDir(), "ancestor-local-plugin")
	writePreviewFixturePlugin(t, ancestorPluginDir, "ancestor-local-fixture", filepath.Join(t.TempDir(), "ancestor-marker"))
	if err := os.MkdirAll(filepath.Join(projectRoot, ".evener"), 0o755); err != nil {
		t.Fatalf("create ancestor local config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".evener", "launch.local.toml"), []byte("plugin_dirs = [\""+ancestorPluginDir+"\"]\n"), 0o644); err != nil {
		t.Fatalf("write ancestor local launch config: %v", err)
	}

	launchRoot := t.TempDir()
	if err := os.Mkdir(missingCWD, 0o755); err != nil {
		t.Fatalf("create target for project identity setup: %v", err)
	}
	paths, err := launchconfig.PathsFor(launchRoot, missingCWD)
	if err != nil {
		t.Fatalf("PathsFor target: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.LegacyProject), 0o755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(paths.LegacyProject, []byte("plugin_dirs = [\""+projectPluginDir+"\"]\n"), 0o644); err != nil {
		t.Fatalf("write project launch config: %v", err)
	}
	if err := os.Remove(missingCWD); err != nil {
		t.Fatalf("remove target before preview: %v", err)
	}

	pluginRoot := t.TempDir()
	ctl := newHubPluginsController(pluginRoot, launchRoot)
	preview, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: missingCWD})
	if err != nil {
		t.Fatalf("Preview for missing non-Git cwd: %v", err)
	}
	if len(preview.Plugins) != 1 || preview.Plugins[0].Name != "project-fixture" {
		t.Fatalf("Preview plugins = %+v, want only project-fixture", preview.Plugins)
	}
	if _, err := os.Stat(missingCWD); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Preview created missing cwd: stat=%v", err)
	}

	if err := os.Mkdir(missingCWD, 0o755); err != nil {
		t.Fatalf("create cwd: %v", err)
	}
	spawner := &recordingSpawner{}
	_, err = hubThreadStart(context.Background(), hubcore.WebConfig{
		LaunchConfigRoot: launchRoot,
		PluginRoot:       pluginRoot,
		Spawner:          spawner,
	}, appsource.NewRegistry(), appwire.ThreadStartParams{
		CWD:   missingCWD,
		Model: "openai/gpt-5",
	})
	if err != nil {
		t.Fatalf("ThreadStart after creating cwd: %v", err)
	}
	spawns := spawner.Spawns()
	if len(spawns) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawns))
	}
	if got := spawns[0].Resolved.Effective.PluginDirs; len(got) != 1 || got[0] != projectPluginDir {
		t.Fatalf("started plugin dirs = %v, want [%q]", got, projectPluginDir)
	}
}

func TestPluginPreviewMissingChildUsesProjectLayerPluginDirs(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	missingCWD := filepath.Join(projectRoot, "new-session")
	pluginDir := filepath.Join(t.TempDir(), "project-plugin")
	writePreviewFixturePlugin(t, pluginDir, "project-fixture", filepath.Join(t.TempDir(), "marker"))
	ancestorLocalPluginDir := filepath.Join(t.TempDir(), "ancestor-local-plugin")
	writePreviewFixturePlugin(t, ancestorLocalPluginDir, "ancestor-local-fixture", filepath.Join(t.TempDir(), "marker"))
	if err := os.MkdirAll(filepath.Join(projectRoot, ".evener"), 0o755); err != nil {
		t.Fatalf("create ancestor local config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".evener", "launch.local.toml"), []byte("plugin_dirs = [\""+ancestorLocalPluginDir+"\"]\n"), 0o644); err != nil {
		t.Fatalf("write ancestor local launch config: %v", err)
	}
	launchRoot := t.TempDir()
	paths, err := launchconfig.PathsFor(launchRoot, projectRoot)
	if err != nil {
		t.Fatalf("PathsFor project root: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.LegacyProject), 0o755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(paths.LegacyProject, []byte("plugin_dirs = [\""+pluginDir+"\"]\n"), 0o644); err != nil {
		t.Fatalf("write project launch config: %v", err)
	}

	pluginRoot := t.TempDir()
	ctl := newHubPluginsController(pluginRoot, launchRoot)
	preview, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{CWD: missingCWD})
	if err != nil {
		t.Fatalf("Preview for missing child cwd: %v", err)
	}
	if len(preview.Plugins) != 1 || preview.Plugins[0].Name != "project-fixture" {
		t.Fatalf("Preview plugins = %+v, want project-fixture from project layer", preview.Plugins)
	}
	if _, err := os.Stat(missingCWD); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Preview created missing child cwd: stat=%v", err)
	}

	if err := os.Mkdir(missingCWD, 0o755); err != nil {
		t.Fatalf("create cwd: %v", err)
	}
	realResolved, err := launchconfig.Resolve(launchRoot, missingCWD, launchconfig.Layer{})
	if err != nil {
		t.Fatalf("Resolve after creating cwd: %v", err)
	}
	realResolution, err := plugins.NewManager(pluginRoot).ResolveForLaunch(realResolved.Effective.PluginDirs, realResolved.Effective.EnabledPlugins)
	if err != nil {
		t.Fatalf("ResolveForLaunch after creating cwd: %v", err)
	}
	if len(realResolution.Candidates) != 1 || realResolution.Candidates[0].Name != preview.Plugins[0].Name {
		t.Fatalf("real-directory plugins = %+v, want same project-fixture inventory as preview", realResolution.Candidates)
	}
}

func TestPluginPreviewControllerMapsFullResolution(t *testing.T) {
	pluginRoot := t.TempDir()
	launchRoot := t.TempDir()
	ctl := newHubPluginsController(pluginRoot, launchRoot)
	marketplaceRoot := t.TempDir()
	firstDir := marketplaceRoot + "/plugins/preview-fixture"
	secondDir := marketplaceRoot + "/plugins/other-fixture"
	writePreviewFixturePlugin(t, firstDir, "preview-fixture", t.TempDir()+"/first-marker")
	writePreviewFixturePlugin(t, secondDir, "other-fixture", t.TempDir()+"/second-marker")
	writeTestMarketplaceManifest(t, marketplaceRoot, "acme", `[{"name":"preview-fixture","description":"preview only","source":"./plugins/preview-fixture"},{"name":"other-fixture","description":"other preview","source":"./plugins/other-fixture"}]`)
	addTestMarketplace(t, ctl, marketplaceRoot)
	for _, name := range []string{"preview-fixture", "other-fixture"} {
		if _, err := ctl.Install(context.Background(), appwire.PluginRefParams{Plugin: name, Marketplace: "acme"}); err != nil {
			t.Fatalf("Install %s: %v", name, err)
		}
	}
	installed, err := ctl.ListPlugins()
	if err != nil || len(installed.Plugins) != 2 {
		t.Fatalf("ListPlugins = %+v, err=%v", installed.Plugins, err)
	}
	installedPaths := make(map[string]string, len(installed.Plugins))
	for _, entry := range installed.Plugins {
		installedPaths[entry.Plugin] = entry.InstallPath
	}
	invalidDir := t.TempDir()
	selected := []string{"preview-fixture", "missing-fixture"}
	resp, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{
		CWD: t.TempDir(),
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{invalidDir},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(resp.Plugins) != 2 {
		t.Fatalf("Preview plugins = %+v, want both installed candidates", resp.Plugins)
	}
	byName := make(map[string]appwire.PluginLaunchCandidate, len(resp.Plugins))
	for _, candidate := range resp.Plugins {
		byName[candidate.Name] = candidate
	}
	first := byName["preview-fixture"]
	if first.Name != "preview-fixture" || first.Version != "1.2.3" || first.Description != "preview only" || first.Source != "installed" || first.Marketplace != "acme" || first.Path != installedPaths["preview-fixture"] || !first.Selected || first.SkillCount != 1 || first.AgentCount != 1 || first.CommandCount != 1 || first.HookCount != 1 || first.MCPCount != 1 {
		t.Fatalf("mapped selected candidate = %+v", first)
	}
	other := byName["other-fixture"]
	if other.Name != "other-fixture" || other.Version != "1.2.3" || other.Description != "preview only" || other.Source != "installed" || other.Marketplace != "acme" || other.Selected || other.SkillCount != 1 || other.AgentCount != 1 || other.CommandCount != 1 || other.HookCount != 1 || other.MCPCount != 1 {
		t.Fatalf("mapped unselected candidate = %+v", other)
	}
	if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Name != "" || resp.Diagnostics[0].Path != invalidDir || resp.Diagnostics[0].Source != "directory" || resp.Diagnostics[0].Message == "" {
		t.Fatalf("mapped diagnostics = %+v", resp.Diagnostics)
	}
	if len(resp.SelectionErrors) != 1 || resp.SelectionErrors[0] != (appwire.PluginSelectionError{Name: "missing-fixture", Reason: "no valid plugin candidate"}) {
		t.Fatalf("mapped selection errors = %+v", resp.SelectionErrors)
	}
}

func TestPluginSelectionPreviewUsesOnlySelectedConcreteDirectories(t *testing.T) {
	root := t.TempDir()
	selectedDir := filepath.Join(root, "selected")
	excludedDir := filepath.Join(root, "excluded")
	writePreviewFixturePlugin(t, selectedDir, "selected", filepath.Join(root, "selected-marker"))
	writePreviewFixturePlugin(t, excludedDir, "excluded", filepath.Join(root, "excluded-marker"))
	ctl := newHubPluginsController(t.TempDir(), t.TempDir())
	selected := []string{"selected"}
	resp, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{
		CWD: t.TempDir(),
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{selectedDir, excludedDir},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(resp.Plugins) != 2 || !resp.Plugins[0].Selected {
		// Candidate order follows explicit directory order; assert the complete
		// selection shape below so this failure cannot be confused with ordering.
		t.Fatalf("preview candidates = %+v", resp.Plugins)
	}
	for _, candidate := range resp.Plugins {
		switch candidate.Name {
		case "selected":
			if !candidate.Selected {
				t.Fatalf("selected candidate was not selected: %+v", candidate)
			}
		case "excluded":
			if candidate.Selected {
				t.Fatalf("excluded candidate was selected: %+v", candidate)
			}
		default:
			t.Fatalf("unexpected candidate: %+v", candidate)
		}
	}
}

// Previewing a bundled plugin by name only inspects it: nothing is published,
// so an abandoned preview leaves nothing behind.
func TestPluginPreviewBundledPluginPublishesNothing(t *testing.T) {
	pluginRoot := t.TempDir()
	ctl := newHubPluginsController(pluginRoot, t.TempDir())
	names := []string{"coordinator-workflow"}
	got, err := ctl.Preview(context.Background(), appwire.PluginPreviewParams{
		CWD: t.TempDir(), LaunchOverrides: &appwire.LaunchConfigLayer{EnabledPlugins: &names},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SelectionErrors) != 0 || len(got.Plugins) != 1 || got.Plugins[0].Source != "bundled" || !got.Plugins[0].Selected {
		t.Fatalf("preview = %+v, want the bundled coordinator-workflow selected", got)
	}
	entries, err := os.ReadDir(filepath.Join(pluginRoot, "bundled"))
	if err != nil {
		t.Fatalf("read the bundled store after a preview: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("preview left %d entries in the plugin store: %v", len(entries), entries)
	}
}
