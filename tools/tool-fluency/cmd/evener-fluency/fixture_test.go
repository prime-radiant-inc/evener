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

func TestMaterializeFixtureRejectsUntrackedPathEscape(t *testing.T) {
	t.Parallel()
	err := materializeFixture(filepath.Join(t.TempDir(), "work"), fixtureSpec{
		Untracked: map[string]string{"../escape": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("err = %v, want a path-escape error", err)
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
