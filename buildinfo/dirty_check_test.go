package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// make/building.mk computes the GitDirty link flag by shelling out to git at
// recipe time, right after build-web completes. That is exactly when the SPA
// build has deleted and re-created the tracked
// cmd/evener-hub/frontend/dist/PLACEHOLDER (scripts/clean-dist.mjs wipes dist,
// vite's emptyOutDir wipes it again and its closeBundle hook writes the file
// back), so the index's cached stat for the file is stale — and because the
// check runs with --no-optional-locks, git can never refresh it and reads the
// byte-identical file as modified. Every `make build` from a clean tree
// stamped its binary "-dirty" that way (issue #3665).
//
// These tests hold the contract from the makefile's side: the dirty command
// must stay blind to the placeholder's delete-and-recreate churn while still
// flagging a real edit, so the flag describes the tree the developer invoked
// the build on.

// dirtyCommandFromMakefile extracts the git command make/building.mk runs for
// the GitDirty flag, so the tests run the makefile's own command rather than a
// copy that could drift from it.
func dirtyCommandFromMakefile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "make", "building.mk"))
	if err != nil {
		t.Fatalf("read make/building.mk: %v", err)
	}
	match := regexp.MustCompile(`buildinfo\.GitDirty=\$\$\((.*?) && echo`).FindStringSubmatch(string(data))
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

// runDirtyCommand runs the makefile's GitDirty command in dir and reports
// whether git read the tree as clean.
func runDirtyCommand(t *testing.T, command, dir string) bool {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	return cmd.Run() == nil
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

func TestGitDirtyCommandIgnoresPlaceholderChurn(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if !runDirtyCommand(t, dirtyCommandFromMakefile(t), dir) {
		t.Fatal("the GitDirty command reads the placeholder's delete-and-recreate churn as a modified tree, " +
			"so every make build stamps -dirty (issue #3665)")
	}
}

func TestGitDirtyCommandFlagsRealEdits(t *testing.T) {
	dir := scratchRepo(t)
	churnPlaceholder(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runDirtyCommand(t, dirtyCommandFromMakefile(t), dir) {
		t.Fatal("the GitDirty command missed a real edit to a tracked source file")
	}
}
