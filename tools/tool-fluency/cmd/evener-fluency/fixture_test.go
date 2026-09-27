package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
