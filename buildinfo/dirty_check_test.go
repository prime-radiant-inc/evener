package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// make/building.mk computes the GitDirty link flag by shelling out to git at
// recipe time, right after build-web completes. That is exactly when the SPA
// build has deleted and re-created the tracked
// cmd/evener-hub/frontend/dist/PLACEHOLDER
// (cmd/evener-hub/frontend/scripts/clean-dist.mjs wipes dist, vite's
// emptyOutDir wipes it again and its closeBundle hook writes the file back),
// so the index's cached stat for the file is stale. diff-files is read-only
// plumbing: it never refreshes that cached stat, with or without
// --no-optional-locks — only an index-writing command such as git status
// does, and the build path runs none — so git reads the byte-identical file
// as modified. Every `make build` from a clean tree stamped its binary
// "-dirty" that way (issue #3665).
//
// These tests hold the contract from the makefile's side: the dirty command
// must stay blind to the placeholder's delete-and-recreate churn while still
// flagging a real edit, so the flag describes the tree the developer invoked
// the build on.

// repositorySelectionEnvironment mirrors identifier/git.go's
// filteredGitEnvironment (see identifier/git_test.go): ambient values for
// these variables would redirect the tests' git and sh subprocesses into an
// unrelated repository.
var repositorySelectionEnvironment = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

func hermeticEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
outer:
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		for _, banned := range repositorySelectionEnvironment {
			if strings.EqualFold(key, banned) {
				continue outer
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// dirtyCommandFromMakefile extracts the full command substitution
// make/building.mk runs for the GitDirty flag — the git predicate and the
// ""/"true" mapping — so the tests run the makefile's own logic rather than
// a copy that could drift from it.
func dirtyCommandFromMakefile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "make", "building.mk"))
	if err != nil {
		t.Fatalf("read make/building.mk: %v", err)
	}
	match := regexp.MustCompile(`buildinfo\.GitDirty=\$\$\((.*?)\)`).FindStringSubmatch(string(data))
	if match == nil {
		t.Fatalf("make/building.mk carries no $$(...) GitDirty command to test")
	}
	return match[1]
}

// scratchRepo builds a committed repository holding the tracked placeholder
// and one ordinary source file, the layout the real checkout has.
func scratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = hermeticEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "test")
	placeholder := filepath.Join(dir, "cmd", "evener-hub", "frontend", "dist", "PLACEHOLDER")
	if err := os.MkdirAll(filepath.Dir(placeholder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeholder, []byte("run make build-web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go", "cmd/evener-hub/frontend/dist/PLACEHOLDER")
	git("commit", "-q", "-m", "initial")
	return dir
}

// churnPlaceholder recreates the tracked placeholder byte-identically with a
// moved mtime, the state the SPA build leaves it in: same content, stale
// index stat.
func churnPlaceholder(t *testing.T, dir string) {
	t.Helper()
	placeholder := filepath.Join(dir, "cmd", "evener-hub", "frontend", "dist", "PLACEHOLDER")
	if err := os.Remove(placeholder); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeholder, []byte("run make build-web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(placeholder, future, future); err != nil {
		t.Fatal(err)
	}
}

// gitDirtyValue runs the makefile's GitDirty command in dir and returns the
// flag value it would stamp: "" when the tree reads clean, "true" when a
// tracked file reads modified. Asserting the value, not the command's exit
// status, pins the ""/"true" mapping too.
func gitDirtyValue(t *testing.T, command, dir string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = hermeticEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run GitDirty command %q: %v\n%s", command, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitDirtyCommandIgnoresPlaceholderChurn(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "" {
		t.Fatalf("GitDirty = %q on the placeholder's delete-and-recreate churn, want \"\" — "+
			"as-is, every make build stamps -dirty (issue #3665)", got)
	}
}

func TestGitDirtyCommandFlagsRealEdits(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirtyValue(t, dirtyCommandFromMakefile(t), dir); got != "true" {
		t.Fatalf("GitDirty = %q with a real edit to a tracked source file, want \"true\"", got)
	}
}
