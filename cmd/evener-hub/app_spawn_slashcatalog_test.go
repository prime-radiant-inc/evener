package hub

// Tests for evener/spawn/slashCatalog: the pre-session slash inventory
// (commands + skills) a spawn with the given cwd, harness, and launch
// overrides would offer.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/plugins"
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
	// The wire flags must carry the catalog's real invocation controls and
	// availability: mergeSlashCommands keeps only available && userInvocable
	// rows, so a catalog serializing zero values advertises nothing.
	byName := make(map[string]appwire.EvenerSkillInfo, len(resp.Skills))
	for _, s := range resp.Skills {
		byName[s.Name] = s
	}
	for _, name := range []string{"deployskill", "greeter:helper"} {
		s, ok := byName[name]
		if !ok {
			continue // asserted missing above
		}
		if !s.Available || !s.UserInvocable || s.DisableModelInvocation {
			t.Errorf("skill %q wire flags = {Available:%v UserInvocable:%v DisableModelInvocation:%v}, want available user-invocable",
				name, s.Available, s.UserInvocable, s.DisableModelInvocation)
		}
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

func TestHubSpawnSlashCatalog_NonfatalResolverErrorKeepsEffectivePluginDirs(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	pluginDir := t.TempDir()
	writeSlashCatalogPlugin(t, pluginDir, "greeter", "greet")

	// A nonfatal resolver error (no selection to honor, live context) must
	// not empty the catalog: thread/start launches with the effective plugin
	// dirs on Resolved, so the catalog keeps those same dirs and shows what
	// the resulting session loads.
	origResolve := hubResolvePlugins
	hubResolvePlugins = func(ctx context.Context, pluginRoot string, dirs []string, enabled *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{}, errors.New("registry unreachable")
	}
	t.Cleanup(func() { hubResolvePlugins = origResolve })

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             t.TempDir(),
		LaunchOverrides: &appwire.LaunchConfigLayer{PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v, want fallthrough on a nonfatal resolver error", err)
	}
	greet := slashCatalogCommandByName(t, resp, "greet")
	if greet.Source != "plugin" {
		t.Errorf("greet Source = %q, want %q (effective plugin dirs retained)", greet.Source, "plugin")
	}
}

func TestHubSpawnSlashCatalog_EmptyCWDScansConfiguredSkillsDirs(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	extraSkills := t.TempDir()
	writeSlashCatalogSkill(t, extraSkills, "extras", "extras", "Extra skills")

	// An empty cwd returns the user-level inventory, but configured extra
	// skill directories are cwd-independent: a session loads them whatever
	// the cwd, so the catalog must include them too. (DiscoverSkills with a
	// nil env returns nil without scanning extraDirs.)
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             "",
		LaunchOverrides: &appwire.LaunchConfigLayer{SkillsDirs: []string{extraSkills}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	if _, ok := slashCatalogSkillNames(resp)["extras"]; !ok {
		t.Errorf("configured skill %q missing from empty-cwd catalog: %v", "extras", slashCatalogSkillNames(resp))
	}
}

func TestHubSpawnSlashCatalog_MissingCWDFallsBackToUserLevelInventory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	parent := t.TempDir()
	missing := filepath.Join(parent, "not-yet-created")
	pluginDir := t.TempDir()
	writeSlashCatalogPlugin(t, pluginDir, "greeter", "greet")

	// The spawn flow creates a missing directory via preflightDir ("Create &
	// start"), so the catalog must not fail for it: the nearest existing
	// ancestor (bare here, so no project items) resolves, and explicit plugin
	// dirs still surface.
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD:             missing,
		LaunchOverrides: &appwire.LaunchConfigLayer{PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v, want user-level fallthrough for a not-yet-created cwd", err)
	}
	greet := slashCatalogCommandByName(t, resp, "greet")
	if greet.Source != "plugin" {
		t.Errorf("greet Source = %q, want %q", greet.Source, "plugin")
	}
}

func TestHubSpawnSlashCatalog_MissingCWDSeesAncestorProjectInventory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	parent := t.TempDir()
	hubTestMakeGitRepo(t, parent, "README.md", "repo")
	writeSlashCatalogProjectCommand(t, parent, "deploy", "Deploy the thing")
	writeSlashCatalogSkill(t, filepath.Join(parent, "skills"), "deployskill", "deployskill", "Deploys stuff")
	missing := filepath.Join(parent, "not-yet-created")

	// The spawn flow creates a missing directory via preflightDir ("Create &
	// start"), and thread/start then loads the ancestor chain's project items
	// (chain discovery from the git root) — so the catalog resolves the
	// missing target through a probe under the nearest existing ancestor and
	// shows the same chain. A bare temp dir is not a git repo, so this needs
	// a real one: without it the chain is the leaf alone.
	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD: missing,
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v, want ancestor-chain inventory for a not-yet-created cwd", err)
	}
	deploy := slashCatalogCommandByName(t, resp, "deploy")
	if deploy.Source != "project" {
		t.Errorf("deploy Source = %q, want %q", deploy.Source, "project")
	}
	if _, ok := slashCatalogSkillNames(resp)["deployskill"]; !ok {
		t.Errorf("project skill %q missing from missing-cwd catalog: %v", "deployskill", slashCatalogSkillNames(resp))
	}
}

func TestHubSpawnSlashCatalog_MissingCWDOutsideRepoIsUserLevelOnly(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	parent := t.TempDir()
	// No git repo: chain discovery is the leaf alone, and the missing leaf
	// is empty — so (like thread/start at the created-then-empty target) the
	// honest inventory is user-level-only. Ancestor project files must not
	// leak in.
	writeSlashCatalogProjectCommand(t, parent, "deploy", "Deploy the thing")
	missing := filepath.Join(parent, "not-yet-created")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD: missing,
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	for _, cmd := range resp.Commands {
		if cmd.Name == "deploy" {
			t.Errorf("non-repo ancestor command %q leaked into missing-cwd catalog", cmd.Name)
		}
	}
}

func TestHubSpawnSlashCatalog_FileCWDIsInvalidParams(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD: file,
	}); err == nil {
		t.Fatal("hubSpawnSlashCatalog with a file cwd = nil error, want InvalidParams")
	}
}

func TestHubSpawnSlashCatalog_MissingCWDDoesNotLeakAncestorLocalConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	parent := t.TempDir()
	hubTestMakeGitRepo(t, parent, "README.md", "repo")
	// Ancestor chain items DO surface...
	writeSlashCatalogProjectCommand(t, parent, "deploy", "Deploy the thing")
	// ...but the ancestor's own cwd-anchored launch.local.toml must NOT:
	// thread/start at the (empty) target reads target/.evener/launch.local.toml
	// (absent), never the ancestor's.
	leakSkills := t.TempDir()
	writeSlashCatalogSkill(t, leakSkills, "leaked", "leaked", "Must not appear")
	evenerDir := filepath.Join(parent, ".evener")
	if err := os.MkdirAll(evenerDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evenerDir, "launch.local.toml"),
		[]byte(`skills_dirs = ["`+leakSkills+`"]`), 0644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "not-yet-created")

	resp, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{
		CWD: missing,
	})
	if err != nil {
		t.Fatalf("hubSpawnSlashCatalog: %v", err)
	}
	slashCatalogCommandByName(t, resp, "deploy")
	if _, ok := slashCatalogSkillNames(resp)["leaked"]; ok {
		t.Errorf("ancestor launch.local.toml skill %q leaked into missing-cwd catalog", "leaked")
	}
}
