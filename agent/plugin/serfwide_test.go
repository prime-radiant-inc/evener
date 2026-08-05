package plugin

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// writeSerfwideCommand writes dir/<name>.md with content and returns dir.
func writeSerfwideCommand(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSerfWideCommands_UserGlobalOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "serf", "commands")
	writeSerfwideCommand(t, global, "review", "global body")

	got, warnings := DiscoverSerfWideCommands(nil) // nil env: no project walk
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	cmd, ok := got["review"]
	if !ok {
		t.Fatalf("no command %q; got keys %v", "review", maps.Keys(got))
	}
	if cmd.Source != "user" || cmd.Body != "global body" {
		t.Errorf("got %+v, want Source=user Body=%q", cmd, "global body")
	}
}

func TestDiscoverSerfWideCommands_ProjectShadowsUser(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeSerfwideCommand(t, filepath.Join(xdg, "serf", "commands"), "review", "global body")

	workDir := t.TempDir() // not a git repo: root == cwd
	writeSerfwideCommand(t, filepath.Join(workDir, ".serf", "commands"), "review", "project body")

	env := execenv.NewLocalExecutionEnvironment(workDir)
	got, _ := DiscoverSerfWideCommands(env)
	cmd := got["review"]
	if cmd.Source != "project" || cmd.Body != "project body" {
		t.Errorf("got %+v, want project command shadowing user-global", cmd)
	}
}

func TestDiscoverSerfWideCommands_ProjectGitRootToCwdOrdering(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeSerfwideCommand(t, filepath.Join(xdg, "serf", "commands"), "review", "global body")

	root := t.TempDir()
	cwd := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSerfwideCommand(t, filepath.Join(root, ".serf", "commands"), "review", "root body")
	writeSerfwideCommand(t, filepath.Join(root, "one", ".serf", "commands"), "review", "one body")
	writeSerfwideCommand(t, filepath.Join(cwd, ".serf", "commands"), "review", "cwd body")
	writeSerfwideCommand(t, filepath.Join(root, ".serf", "commands"), "root-only", "root-only body")

	got, warnings := DiscoverSerfWideCommands(execenv.NewLocalExecutionEnvironment(cwd))
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if cmd := got["review"]; cmd.Source != "project" || cmd.Body != "cwd body" {
		t.Errorf("review = %+v, want deepest project command", cmd)
	}
	if cmd := got["root-only"]; cmd.Source != "project" || cmd.Body != "root-only body" {
		t.Errorf("root-only = %+v, want root project command", cmd)
	}
}

func TestDiscoverSerfWideCommands_EmptyWorkingDirectoryStillScansUser(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "serf", "commands")
	writeSerfwideCommand(t, global, "review", "global body")

	env := execenv.NewLocalExecutionEnvironment("")
	got, warnings := DiscoverSerfWideCommands(env)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if cmd := got["review"]; cmd.Source != "user" || cmd.Body != "global body" {
		t.Errorf("review = %+v, want user-global command", cmd)
	}
}

func TestDiscoverSerfWideCommands_IgnoresNonMarkdown(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "serf", "commands")
	writeSerfwideCommand(t, dir, "review", "body")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, warnings := DiscoverSerfWideCommands(nil)
	if len(got) != 1 || len(warnings) != 0 {
		t.Errorf("got %d commands, %d warnings; want 1, 0", len(got), len(warnings))
	}
}

func TestDiscoverSerfWideCommands_IsolatedXDGConfigHome(t *testing.T) {
	first := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", first)
	writeSerfwideCommand(t, filepath.Join(first, "serf", "commands"), "first", "first body")
	got, warnings := DiscoverSerfWideCommands(nil)
	if len(warnings) != 0 {
		t.Fatalf("first scan warnings = %v, want none", warnings)
	}
	if _, ok := got["first"]; !ok {
		t.Fatalf("first scan missing first command: %v", maps.Keys(got))
	}

	second := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", second)
	writeSerfwideCommand(t, filepath.Join(second, "serf", "commands"), "second", "second body")
	got, warnings = DiscoverSerfWideCommands(nil)
	if len(warnings) != 0 {
		t.Fatalf("second scan warnings = %v, want none", warnings)
	}
	if _, ok := got["second"]; !ok {
		t.Fatalf("second scan missing second command: %v", maps.Keys(got))
	}
	if _, ok := got["first"]; ok {
		t.Fatalf("second scan leaked first command: %v", maps.Keys(got))
	}
}
