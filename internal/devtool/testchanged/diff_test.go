package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// changedFiles must see a file the change added but never staged, because a
// new package is untracked until someone runs `git add`. Omitting it made
// `make test-changed` report "diff touches no Go package" for exactly the
// change it exists to test, so this case pins that set independently of the
// package graph.
func TestChangedFilesIncludesUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "test")

	tracked := filepath.Join(root, "tracked.go")
	if err := os.WriteFile(tracked, []byte("package tracked\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", tracked, err)
	}
	gitIn(t, root, "add", "tracked.go")
	gitIn(t, root, "commit", "-q", "-m", "base")

	committed := filepath.Join(root, "committed.go")
	if err := os.WriteFile(committed, []byte("package committed\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", committed, err)
	}
	gitIn(t, root, "add", "committed.go")
	gitIn(t, root, "commit", "-q", "-m", "add committed")

	untracked := filepath.Join(root, "untracked.go")
	if err := os.WriteFile(untracked, []byte("package untracked\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", untracked, err)
	}
	// An ignored file must not be swept in with the untracked ones: it is not
	// part of the change and its package is unaffected.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package ignored\n"), 0o644); err != nil {
		t.Fatalf("writing ignored.go: %v", err)
	}

	base, err := gitOutput(root, "rev-parse", "HEAD~1")
	if err != nil {
		t.Fatalf("resolving base: %v", err)
	}

	got, err := changedFiles(root, strings.TrimRight(base, "\r\n"))
	if err != nil {
		t.Fatalf("changedFiles: %v", err)
	}

	want := []string{"committed.go", "untracked.go", ".gitignore"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("changedFiles = %v, missing %q", got, w)
		}
	}
	if slices.Contains(got, "ignored.go") {
		t.Errorf("changedFiles = %v, should not include the ignored file", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
