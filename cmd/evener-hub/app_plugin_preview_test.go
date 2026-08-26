package hub

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
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
