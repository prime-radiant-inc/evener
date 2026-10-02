package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMaterializeFixtureGitCommitsFilesAndLeavesUntrackedOut: a git fixture is
// a repository on main whose one commit holds the fixture files, with the
// untracked files written after it, and with a local identity so the agent's
// own commits work on a machine that has none.
func TestMaterializeFixtureGitCommitsFilesAndLeavesUntrackedOut(t *testing.T) {
	t.Parallel()
	work := filepath.Join(t.TempDir(), "work")
	err := materializeFixture(work, fixtureSpec{
		Files:     map[string]string{"main.go": "package main\n"},
		Git:       true,
		Untracked: map[string]string{"notes.txt": "mine\n"},
	})
	if err != nil {
		t.Fatalf("materializeFixture: %v", err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"branch", "--show-current"}, "main"},
		{[]string{"ls-files"}, "main.go"},
		{[]string{"status", "--porcelain"}, "?? notes.txt"},
		{[]string{"rev-list", "--count", "HEAD"}, "1"},
		{[]string{"config", "user.email"}, "fixture@evener.test"},
		{[]string{"config", "core.hooksPath"}, ".git/hooks"},
	} {
		if got := fixtureGit(t, work, c.args...); got != c.want {
			t.Errorf("git %s = %q, want %q", strings.Join(c.args, " "), got, c.want)
		}
	}
}

// TestMaterializeFixtureIgnoresTheUsersGitSetup: the user's global git setup
// belongs to their own work. A fixture still commits every file, and the
// agent's own commits still work, when that setup signs every commit, installs
// hooks through core.hooksPath or init.templateDir, and ignores files the
// fixture holds. Not parallel: it sets GIT_CONFIG_GLOBAL.
func TestMaterializeFixtureIgnoresTheUsersGitSetup(t *testing.T) {
	home := t.TempDir()
	for _, hook := range []string{filepath.Join(home, "hooks", "pre-commit"), filepath.Join(home, "template", "hooks", "pre-commit")} {
		mustWrite(t, hook, "#!/bin/sh\necho \"a user hook ran\" >&2\nexit 1\n")
		if err := os.Chmod(hook, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(home, "ignore"), "*.txt\n")
	global := filepath.Join(home, "gitconfig")
	mustWrite(t, global, "[commit]\n\tgpgsign = true\n[tag]\n\tgpgSign = true\n\tforceSignAnnotated = true\n[gpg]\n\tprogram = false\n"+
		"[core]\n\thooksPath = "+filepath.Join(home, "hooks")+"\n\texcludesFile = "+filepath.Join(home, "ignore")+"\n"+
		"[init]\n\ttemplateDir = "+filepath.Join(home, "template")+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, fixtureSpec{Files: map[string]string{"notes.txt": "kept\n"}, Git: true}); err != nil {
		t.Fatalf("materializeFixture: %v", err)
	}
	if got := fixtureGit(t, work, "ls-files"); got != "notes.txt" {
		t.Errorf("tracked files = %q, want notes.txt", got)
	}
	mustWrite(t, filepath.Join(work, "more.txt"), "the agent's change\n")
	fixtureGit(t, work, "add", "more.txt")
	fixtureGit(t, work, "commit", "-q", "-m", "the agent's commit")
	fixtureGit(t, work, "tag", "-a", "v1.0.0", "-m", "the agent's annotated tag")
}

func TestMaterializeFixtureRejectsUntrackedPathEscape(t *testing.T) {
	t.Parallel()
	err := materializeFixture(filepath.Join(t.TempDir(), "work"), fixtureSpec{
		Untracked: map[string]string{"../escape": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("err = %v, want a path-escape error", err)
	}
}

// TestMaterializeFixtureCommitDisablesAutoMaintenance: since git 2.5x a commit
// runs `git maintenance run --auto --detach` (run-command.c:
// prepare_auto_maintenance). That writer lives in its own session, outlives the
// commit, and writes into the fixture's .git — recreating it if t.TempDir()
// cleanup already removed it, so cleanup then fails with "directory not empty".
// The fixture must commit with maintenance.auto=false, as the worktree tools
// already do, so no detached writer is left to race the cleanup.
func TestMaterializeFixtureCommitDisablesAutoMaintenance(t *testing.T) {
	t.Parallel()
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, fixtureSpec{Git: true, Files: map[string]string{"main.go": "package main\n"}}); err != nil {
		t.Fatalf("materializeFixture: %v", err)
	}
	cmd := exec.Command("git", "config", "--get", "maintenance.auto")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the fixture repository does not set maintenance.auto, so on git 2.5x its commit leaves a detached `git maintenance run --auto` writer that outlives it and races t.TempDir() cleanup: git config --get maintenance.auto: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "false" {
		t.Errorf("fixture maintenance.auto = %q, want false so the fixture's commit leaves no detached maintenance writer", got)
	}
}

// TestRunCheckCutsTheFixtureOffFromAnEnclosingRepository: results can land
// inside a repository with a go.work, such as evener's own. A check still sees
// the fixture as its own Go module and finds no git repository above it.
func TestRunCheckCutsTheFixtureOffFromAnEnclosingRepository(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	mustWrite(t, filepath.Join(outer, "go.work"), "go 1.22\n\nuse ./other\n")
	mustWrite(t, filepath.Join(outer, "other", "go.mod"), "module example.com/other\n\ngo 1.22\n")
	fixtureGit(t, outer, "init", "-q")
	work := filepath.Join(outer, "results", "work")
	mustWrite(t, filepath.Join(work, "go.mod"), "module example.com/fixture\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(work, "fixture.go"), "package fixture\n")
	for _, check := range []checkSpec{
		{Name: "builds as its own module", Run: "go build ./..."},
		{Name: "no repository above", Run: "! git rev-parse --git-dir"},
	} {
		if ok, detail := runCheck(work, check, time.Minute); !ok {
			t.Errorf("%s: %s", check.Name, detail)
		}
	}
}

// TestRunCLIProbeCutsTheFixtureOffFromAnEnclosingRepository: the agent's
// evener process gets the same isolation as the checks.
func TestRunCLIProbeCutsTheFixtureOffFromAnEnclosingRepository(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "fake-evener")
	mustWrite(t, bin, "#!/bin/sh\nprintf '%s|%s\\n' \"$GOWORK\" \"$GIT_CEILING_DIRECTORIES\"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "rep-01", "work")
	var stdout, stderr bytes.Buffer
	err := runCLIProbe(context.Background(), runConfig{evenerBin: bin, model: "openai/m", reasoningEffort: "low"},
		probeFile{Prompt: "p"}, probeResult{WorkDir: work, StateDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runCLIProbe: %v: %s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "off|"+filepath.Dir(work); got != want {
		t.Errorf("evener saw GOWORK|GIT_CEILING_DIRECTORIES = %q, want %q", got, want)
	}
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
