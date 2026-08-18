package plugin

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
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

func TestDiscoverEvenerWideCommands_UserGlobalOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "commands")
	writeSerfwideCommand(t, global, "review", "global body")

	got, warnings := DiscoverEvenerWideCommands(nil) // nil env: no project walk
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

func TestDiscoverEvenerWideCommands_ProjectShadowsUser(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeSerfwideCommand(t, filepath.Join(xdg, "evener", "commands"), "review", "global body")

	workDir := t.TempDir() // not a git repo: root == cwd
	writeSerfwideCommand(t, filepath.Join(workDir, ".evener", "commands"), "review", "project body")

	env := execenv.NewLocalExecutionEnvironment(workDir)
	got, _ := DiscoverEvenerWideCommands(env)
	cmd := got["review"]
	if cmd.Source != "project" || cmd.Body != "project body" {
		t.Errorf("got %+v, want project command shadowing user-global", cmd)
	}
}

func TestDiscoverEvenerWideCommands_ProjectGitRootToCwdOrdering(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeSerfwideCommand(t, filepath.Join(xdg, "evener", "commands"), "review", "global body")

	root := t.TempDir()
	cwd := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSerfwideCommand(t, filepath.Join(root, ".evener", "commands"), "review", "root body")
	writeSerfwideCommand(t, filepath.Join(root, "one", ".evener", "commands"), "review", "one body")
	writeSerfwideCommand(t, filepath.Join(cwd, ".evener", "commands"), "review", "cwd body")
	writeSerfwideCommand(t, filepath.Join(root, ".evener", "commands"), "root-only", "root-only body")

	got, warnings := DiscoverEvenerWideCommands(execenv.NewLocalExecutionEnvironment(cwd))
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

func TestDiscoverEvenerWideCommands_EmptyWorkingDirectoryStillScansUser(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "commands")
	writeSerfwideCommand(t, global, "review", "global body")

	env := execenv.NewLocalExecutionEnvironment("")
	got, warnings := DiscoverEvenerWideCommands(env)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if cmd := got["review"]; cmd.Source != "user" || cmd.Body != "global body" {
		t.Errorf("review = %+v, want user-global command", cmd)
	}
}

func TestDiscoverEvenerWideCommands_IgnoresNonMarkdown(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "evener", "commands")
	writeSerfwideCommand(t, dir, "review", "body")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, warnings := DiscoverEvenerWideCommands(nil)
	if len(got) != 1 || len(warnings) != 0 {
		t.Errorf("got %d commands, %d warnings; want 1, 0", len(got), len(warnings))
	}
}

func TestDiscoverEvenerWideCommands_IsolatedXDGConfigHome(t *testing.T) {
	first := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", first)
	writeSerfwideCommand(t, filepath.Join(first, "evener", "commands"), "first", "first body")
	got, warnings := DiscoverEvenerWideCommands(nil)
	if len(warnings) != 0 {
		t.Fatalf("first scan warnings = %v, want none", warnings)
	}
	if _, ok := got["first"]; !ok {
		t.Fatalf("first scan missing first command: %v", maps.Keys(got))
	}

	second := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", second)
	writeSerfwideCommand(t, filepath.Join(second, "evener", "commands"), "second", "second body")
	got, warnings = DiscoverEvenerWideCommands(nil)
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

func TestDiscoverEvenerWideCommands_RejectsBadNames(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "evener", "commands")
	writeSerfwideCommand(t, dir, "ok", "body")
	writeSerfwideCommand(t, dir, "p:forge", "body")    // colon: namespace forgery
	writeSerfwideCommand(t, dir, "my command", "body") // whitespace: uninvokable
	writeSerfwideCommand(t, dir, "", "body")           // file named exactly ".md"

	got, warnings := DiscoverEvenerWideCommands(nil)
	if len(got) != 1 {
		t.Errorf("got keys %v, want only [ok]", maps.Keys(got))
	}
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want 3 (colon, whitespace, empty): %v", len(warnings), warnings)
	}
}

func TestDiscoverEvenerWideCommands_MalformedFrontmatterSkipped(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "evener", "commands")
	writeSerfwideCommand(t, dir, "broken", "---\n[unclosed\n---\nbody")
	got, warnings := DiscoverEvenerWideCommands(nil)
	if len(got) != 0 || len(warnings) != 1 {
		t.Errorf("got %d commands, %d warnings; want 0, 1", len(got), len(warnings))
	}
}

func TestDiscoverEvenerWideCommands_Advisories(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "evener", "commands")
	writeSerfwideCommand(t, dir, "exec", "run !`git status` here")
	writeSerfwideCommand(t, dir, "front", "---\nmodel: gpt-5.2\nallowed-tools:\n  - shell\n---\nbody")
	_, warnings := DiscoverEvenerWideCommands(nil)
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w.Message, dir) {
			t.Errorf("warning %q does not name the file", w.Message)
		}
	}
}
