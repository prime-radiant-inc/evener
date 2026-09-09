package hub

// Tests for evener/spawn/slashCatalog: the pre-session slash inventory
// (commands + skills) a spawn with the given cwd, harness, and launch
// overrides would offer.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func writeSlashCatalogPlugin(t *testing.T, dir, pluginName, commandName string) {
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
	if err := os.WriteFile(filepath.Join(commandsDir, commandName+".md"),
		[]byte("---\ndescription: Command "+commandName+"\n---\nBody"), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

func writeSlashCatalogSkill(t *testing.T, skillsDir, dirName, name, description string) {
	t.Helper()
	dir := filepath.Join(skillsDir, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func writeSlashCatalogProjectCommand(t *testing.T, cwd, name, description string) {
	t.Helper()
	dir := filepath.Join(cwd, ".evener", "commands")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir project commands: %v", err)
	}
	content := "---\ndescription: " + description + "\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatalf("write project command: %v", err)
	}
}

func slashCatalogCommandByName(t *testing.T, resp appwire.SpawnSlashCatalogResponse, name string) appwire.CommandDescriptor {
	t.Helper()
	for _, cmd := range resp.Commands {
		if cmd.Name == name {
			return cmd
		}
	}
	t.Fatalf("command %q not in catalog: %+v", name, resp.Commands)
	return appwire.CommandDescriptor{}
}

func slashCatalogSkillNames(resp appwire.SpawnSlashCatalogResponse) map[string]string {
	out := make(map[string]string, len(resp.Skills))
	for _, s := range resp.Skills {
		out[s.Name] = s.Description
	}
	return out
}

func TestHubSpawnSlashCatalog_ProjectCommandAndSkill(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cwd := t.TempDir()
	writeSlashCatalogProjectCommand(t, cwd, "deploy", "Deploy the thing")
	writeSlashCatalogSkill(t, filepath.Join(cwd, "skills"), "deployskill", "deployskill", "Deploys stuff")
	pluginDir := t.TempDir()
	writeSlashCatalogPlugin(t, pluginDir, "greeter", "greet")
	writeSlashCatalogSkill(t, filepath.Join(pluginDir, "skills"), "helper", "helper", "Helps out")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             cwd,
		LaunchOverrides: &appwire.LaunchConfigLayer{PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	deploy := slashCatalogCommandByName(t, resp, "deploy")
	if deploy.Source != "project" {
		t.Errorf("deploy Source = %q, want %q", deploy.Source, "project")
	}
	greet := slashCatalogCommandByName(t, resp, "greet")
	if greet.Source != "plugin" {
		t.Errorf("greet Source = %q, want %q", greet.Source, "plugin")
	}
	skills := slashCatalogSkillNames(resp)
	if _, ok := skills["deployskill"]; !ok {
		t.Errorf("project skill %q missing from catalog: %v", "deployskill", skills)
	}
	if _, ok := skills["greeter:helper"]; !ok {
		t.Errorf("plugin skill %q missing from catalog: %v", "greeter:helper", skills)
	}
}

func TestHubSpawnSlashCatalog_EmptyCWDIsUserLevelOnly(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	commandsDir := filepath.Join(xdg, "evener", "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "standup.md"), []byte("standup body"), 0644); err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	writeSlashCatalogProjectCommand(t, otherDir, "projectonly", "Only in the other project")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	standup := slashCatalogCommandByName(t, resp, "standup")
	if standup.Source != "user" {
		t.Errorf("standup Source = %q, want %q", standup.Source, "user")
	}
	for _, cmd := range resp.Commands {
		if cmd.Name == "projectonly" {
			t.Errorf("project command %q leaked into the user-level catalog: %+v", cmd.Name, resp.Commands)
		}
	}
}

func TestHubSpawnSlashCatalog_NonEvenerHarnessIsEmpty(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	pluginDir := t.TempDir()
	writeSlashCatalogPlugin(t, pluginDir, "greeter", "greet")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             t.TempDir(),
		Harness:         "external",
		LaunchOverrides: &appwire.LaunchConfigLayer{PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Errorf("Commands = %+v, want empty for a non-evener harness", resp.Commands)
	}
	if len(resp.Skills) != 0 {
		t.Errorf("Skills = %+v, want empty for a non-evener harness", resp.Skills)
	}
}

func TestHubSpawnSlashCatalog_BrokenPluginDirDoesNotBrickCatalog(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	brokenDir := t.TempDir() // no .claude-plugin/plugin.json at all: Load fails.
	healthyDir := t.TempDir()
	writeSlashCatalogPlugin(t, healthyDir, "greeter", "greet")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             t.TempDir(),
		LaunchOverrides: &appwire.LaunchConfigLayer{PluginDirs: []string{brokenDir, healthyDir}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v, want the broken dir skipped rather than aborting the whole catalog", err)
	}
	greet := slashCatalogCommandByName(t, resp, "greet")
	if greet.Source != "plugin" {
		t.Errorf("greet Source = %q, want %q", greet.Source, "plugin")
	}
}

func TestHubSpawnSlashCatalog_EnabledPluginsSelectionHonored(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dirA := t.TempDir()
	writeSlashCatalogPlugin(t, dirA, "alpha", "alpha-cmd")
	dirB := t.TempDir()
	writeSlashCatalogPlugin(t, dirB, "beta", "beta-cmd")
	selected := []string{"alpha"}

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD: t.TempDir(),
		LaunchOverrides: &appwire.LaunchConfigLayer{
			PluginDirs:     []string{dirA, dirB},
			EnabledPlugins: &selected,
		},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	slashCatalogCommandByName(t, resp, "alpha-cmd")
	for _, cmd := range resp.Commands {
		if cmd.Name == "beta-cmd" {
			t.Errorf("deselected plugin command %q leaked into the catalog: %+v", cmd.Name, resp.Commands)
		}
	}
}
