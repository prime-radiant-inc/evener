package gittest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ambient git state must not reach a fixture: a hostile environment that
// selects another repository, supplies a global config file or injects config
// is ignored by Run.
func TestRunIgnoresAmbientGitEnvironment(t *testing.T) {
	other := t.TempDir()
	globalConfig := filepath.Join(other, "global.config")
	if err := os.WriteFile(globalConfig, []byte("[fixture]\n\tglobal = leaked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, "nope"))
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "fixture.injected")
	t.Setenv("GIT_CONFIG_VALUE_0", "leaked")

	dir := t.TempDir()
	Run(t, dir, "init", "-q")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("repository not created in dir despite ambient GIT_DIR: %v", err)
	}
	if effective := Run(t, dir, "config", "--list"); strings.Contains(effective, "fixture.") {
		t.Fatalf("ambient configuration reached the fixture:\n%s", effective)
	}
}

func TestNewLinkedWorktreeSharesMainRepository(t *testing.T) {
	main, wt := NewLinkedWorktree(t)
	common := Run(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
	want, err := filepath.EvalSymlinks(filepath.Join(main, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(common)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("linked worktree common dir = %q, want main repo %q", got, want)
	}
	if branch := Run(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feat" {
		t.Fatalf("linked worktree branch = %q, want feat", branch)
	}
}

func TestQuiescentCommitArgsInsertsBeforeCommit(t *testing.T) {
	got := QuiescentCommitArgs([]string{"-c", "x=1", "commit", "-m", "m"})
	want := []string{"-c", "x=1", "-c", "maintenance.auto=false", "commit", "-m", "m"}
	if !slices.Equal(got, want) {
		t.Fatalf("QuiescentCommitArgs = %v, want %v", got, want)
	}
	if got := QuiescentCommitArgs([]string{"status"}); !slices.Equal(got, []string{"status"}) {
		t.Fatalf("non-commit args changed: %v", got)
	}
}

func TestMaintenanceProbeUsesEffectiveBoolean(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		active bool
	}{
		{name: "false", args: []string{"-c", "maintenance.auto=false", "commit"}},
		{name: "zero", args: []string{"-c", "maintenance.auto=0", "commit"}},
		{name: "no", args: []string{"-c", "maintenance.auto=no", "commit"}},
		{name: "off", args: []string{"-c", "maintenance.auto=off", "commit"}},
		{name: "case insensitive false", args: []string{"-c", "maintenance.auto=FaLsE", "commit"}},
		{name: "last false", args: []string{"-c", "maintenance.auto=true", "-c", "maintenance.auto=0", "commit"}},
		{name: "true", args: []string{"-c", "maintenance.auto=true", "commit"}, active: true},
		{name: "last true", args: []string{"-c", "maintenance.auto=0", "-c", "maintenance.auto=on", "commit"}, active: true},
		{name: "absent", args: []string{"commit"}, active: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := NewMaintenanceProbe()
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
				t.Fatalf("mkdir objects: %v", err)
			}
			var gotArgs []string
			wrapped := probe.Run(func(_ string, args ...string) (string, error) {
				gotArgs = slices.Clone(args)
				return "", nil
			})
			result := make(chan error, 1)
			go func() {
				_, err := wrapped(dir, test.args...)
				result <- err
			}()
			if test.active {
				<-probe.Ready
				probe.Published()
				if err := <-result; err != nil {
					t.Fatalf("probe run: %v", err)
				}
				<-probe.Exited
				if probe.MutatorErr != nil {
					t.Fatalf("maintenance mutator: %v", probe.MutatorErr)
				}
				if !probe.PublishedWhileActive {
					t.Fatal("expected publication overlap")
				}
				if !slices.Contains(gotArgs, "maintenance.auto=false") {
					t.Fatalf("real runner args = %v, missing quiescent config", gotArgs)
				}
				return
			}
			if err := <-result; err != nil {
				t.Fatalf("probe run: %v", err)
			}
			select {
			case <-probe.Ready:
				t.Fatal("suppressed config launched synthetic maintenance")
			default:
			}
			if !slices.Equal(gotArgs, test.args) {
				t.Fatalf("real runner args = %v, want %v", gotArgs, test.args)
			}
		})
	}
}

// An ambient template directory can install hooks that run during a fixture's
// commits; Run must not honor it.
func TestRunIgnoresAmbientTemplateHooks(t *testing.T) {
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "hook-ran")
	hooks := filepath.Join(scratch, "template", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_TEMPLATE_DIR", filepath.Join(scratch, "template"))

	dir := t.TempDir()
	Run(t, dir, "init", "-q")
	Run(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ambient GIT_TEMPLATE_DIR hook ran during the fixture's commit")
	}
}

// The legacy GIT_CONFIG variable names a config file for `git config`; a
// fixture's config writes must not land in it.
func TestRunIgnoresAmbientGitConfigFile(t *testing.T) {
	external := filepath.Join(t.TempDir(), "external.config")
	t.Setenv("GIT_CONFIG", external)

	dir := t.TempDir()
	Run(t, dir, "init", "-q")
	Run(t, dir, "config", "fixture.key", "value")
	if _, err := os.Stat(external); err == nil {
		t.Fatal("fixture config write reached the file named by ambient GIT_CONFIG")
	}
}
