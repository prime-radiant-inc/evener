package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentplugin "primeradiant.com/evener/agent/plugin"
)

// pluginFixture copies a testdata fixture plugin into a private directory, so
// Inspect exercises the real loader against a real directory without tests
// sharing mutable state through the fixtures themselves.
func pluginFixture(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", name))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// assertFullInstance pins that instance is the full fixture loaded whole: one
// skill, one agent, one command, and one MCP server, each under the namespace
// the loader assigns.
func assertFullInstance(t *testing.T, instance agentplugin.Instance) {
	t.Helper()
	if _, ok := instance.Skills["full:research"]; !ok {
		t.Errorf("Skills = %v, want the full:research key", instance.Skills)
	}
	if _, ok := instance.Agents["full:helper"]; !ok {
		t.Errorf("Agents = %v, want the full:helper key", instance.Agents)
	}
	if _, ok := instance.Commands["full:build"]; !ok {
		t.Errorf("Commands = %v, want the full:build key", instance.Commands)
	}
	var mcpName string
	if len(instance.MCPConfigs) > 0 {
		mcpName = instance.MCPConfigs[0].Name
	}
	if len(instance.MCPConfigs) != 1 || mcpName != "plugin_full_lookup" {
		t.Errorf("MCPConfigs = %+v, want the single plugin_full_lookup server", instance.MCPConfigs)
	}
}

func TestInspect(t *testing.T) {
	t.Run("path target loads every component kind", func(t *testing.T) {
		m := NewManager(filepath.Join(t.TempDir(), "store"))
		instance, err := m.Inspect(context.Background(), InspectTarget{Path: pluginFixture(t, "full")})
		if err != nil {
			t.Fatal(err)
		}
		assertFullInstance(t, instance)
	})

	t.Run("installed pair resolves through the registry", func(t *testing.T) {
		m := NewManager(filepath.Join(t.TempDir(), "store"))
		installed := pluginFixture(t, "full")
		saveTestRegistry(t, m, map[string][]InstallEntry{
			"full@m": {{InstallPath: installed, Version: "1.0.0", Enabled: true}},
		})
		instance, err := m.Inspect(context.Background(), InspectTarget{Plugin: "full", Marketplace: "m"})
		if err != nil {
			t.Fatal(err)
		}
		// The target carried no path, so this Instance can only have come from
		// the registry entry's InstallPath.
		assertFullInstance(t, instance)
	})

	t.Run("broken directory surfaces the load error", func(t *testing.T) {
		m := NewManager(filepath.Join(t.TempDir(), "store"))
		instance, err := m.Inspect(context.Background(), InspectTarget{Path: pluginFixture(t, "broken")})
		if err == nil {
			t.Fatal("Inspect on a directory with no manifest returned no error")
		}
		if !reflect.DeepEqual(instance, agentplugin.Instance{}) {
			t.Errorf("instance = %+v, want the empty Instance alongside the error", instance)
		}
	})

	t.Run("target matching nothing is a not-found error", func(t *testing.T) {
		m := NewManager(filepath.Join(t.TempDir(), "store"))
		instance, err := m.Inspect(context.Background(), InspectTarget{Plugin: "missing", Marketplace: "m"})
		if err == nil {
			t.Fatal("Inspect for an uninstalled pair returned no error")
		}
		if !errors.Is(err, ErrNotInstalled) {
			t.Errorf("error = %v, want it to wrap ErrNotInstalled", err)
		}
		if !reflect.DeepEqual(instance, agentplugin.Instance{}) {
			t.Errorf("instance = %+v, want the empty Instance alongside the error", instance)
		}
	})
}

func TestInspectUnpublishedBundledCandidate(t *testing.T) {
	m := NewManager(t.TempDir())
	preview, err := m.PreviewForLaunch(context.Background(), nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := preview.ValidateSelection(); err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].Source != LaunchPluginSourceBundled {
		t.Fatalf("Candidates = %+v, want one bundled candidate", preview.Candidates)
	}
	path := preview.Candidates[0].Path
	assertUnpublished := func() {
		t.Helper()
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("publication path exists or cannot be checked: %v", err)
		}
		if entries := bundledStoreEntries(t, m.bundledDir()); len(entries) != 0 {
			t.Fatalf("inspection published entries: %v", entries)
		}
		if entries, err := os.ReadDir(m.bundledPublishingDir()); err != nil || len(entries) != 0 {
			t.Fatalf("preview staging not cleaned: entries = %v, err = %v", entries, err)
		}
	}
	assertUnpublished()
	defer assertUnpublished()
	instance, err := m.Inspect(context.Background(), InspectTarget{Path: path})
	if err != nil {
		t.Fatalf("Inspect unpublished bundled candidate: %v", err)
	}
	if instance.Manifest.Name != "coordinator-workflow" {
		t.Fatalf("loaded manifest = %q", instance.Manifest.Name)
	}
	if _, ok := instance.Agents["coordinator-workflow:coordinator"]; !ok {
		t.Errorf("Agents = %v, want the bundled coordinator", instance.Agents)
	}
	if len(instance.Agents) != preview.Candidates[0].AgentCount {
		t.Errorf("loaded %d agents, preview described %d", len(instance.Agents), preview.Candidates[0].AgentCount)
	}
}

func TestInspectDoesNotStageBrokenOrMissingInstalls(t *testing.T) {
	m := NewManager(t.TempDir())
	digest, err := bundledPluginDigest("coordinator-workflow")
	if err != nil {
		t.Fatal(err)
	}
	brokenBundle := m.bundledPluginPath("coordinator-workflow", digest)
	if err := os.MkdirAll(brokenBundle, 0o755); err != nil {
		t.Fatal(err)
	}
	saveTestRegistry(t, m, map[string][]InstallEntry{
		"coordinator-workflow@m": {{InstallPath: filepath.Join(m.cacheDir(), "coordinator-workflow")}},
	})
	for _, target := range []InspectTarget{
		{Path: filepath.Join(m.cacheDir(), "coordinator-workflow")},
		{Path: m.bundledPluginPath("coordinator-workflow", "0000000000000000")},
		{Path: m.bundledPluginPath("not-bundled", digest)},
		{Path: brokenBundle},
		{Plugin: "coordinator-workflow", Marketplace: "m"},
	} {
		if instance, err := m.Inspect(context.Background(), target); err == nil || !reflect.DeepEqual(instance, agentplugin.Instance{}) {
			t.Errorf("Inspect(%+v) = %+v, %v, want empty instance and load error", target, instance, err)
		}
	}
	if _, err := os.Lstat(m.bundledPublishingDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("broken or missing install created staging: %v", err)
	}
}

func TestInspectBatchLoadsOnlyFreshTargets(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "store"))
	registry := map[string][]InstallEntry{}
	for i := range 3 {
		registry[fmt.Sprintf("plugin%d@m", i)] = []InstallEntry{{InstallPath: pluginFixture(t, "full")}}
	}
	saveTestRegistry(t, m, registry)
	original := pluginLoad
	loads := map[string]int{}
	pluginLoad = func(dir string) (agentplugin.Instance, error) {
		loads[dir]++
		return original(dir)
	}
	t.Cleanup(func() { pluginLoad = original })
	for i := range 3 {
		name := fmt.Sprintf("plugin%d", i)
		dir := registry[name+"@m"][0].InstallPath
		// Disk changes after registration must be seen by the fresh target read.
		if err := os.Remove(filepath.Join(dir, "commands", "build.md")); err != nil {
			t.Fatal(err)
		}
		instance, err := m.Inspect(context.Background(), InspectTarget{Plugin: name, Marketplace: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if len(instance.Commands) != 0 {
			t.Fatalf("stale target commands: %v", instance.Commands)
		}
	}
	for dir, count := range loads {
		if count != 1 {
			t.Errorf("loads for %s = %d, want 1", dir, count)
		}
	}
	if len(loads) != 3 {
		t.Fatalf("loaded %d target directories, want 3", len(loads))
	}
}
