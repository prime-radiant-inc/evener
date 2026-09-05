//go:build unix

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An ancestor this process cannot stat says nothing about whether a .git is
// there. Reading the stat error as "no .git anywhere" and memoizing it makes
// the environment call the directory non-git for the rest of its life — the
// same poisoning as a cancelled fork, reached through the filesystem instead of
// through git. Only ErrNotExist is an absence.
func TestGitRootOrEmptyContext_UnreadableAncestorIsNotMemoized(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	// With the directory itself unreadable, the stat of repo/.git fails EACCES
	// rather than reporting the entry absent.
	if err := os.Chmod(repo, 0o000); err != nil {
		t.Fatalf("chmod repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })
	env := NewLocalExecutionEnvironment(base)

	if got := GitRootOrEmptyContext(context.Background(), env, repo); got != "" {
		t.Fatalf("resolution under an unreadable ancestor = %q, want empty", got)
	}

	// The permission clears and the repository is plainly there.
	if err := os.Chmod(repo, 0o755); err != nil {
		t.Fatalf("restore repo mode: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	got := GitRootOrEmptyContext(context.Background(), env, repo)

	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("resolution once the ancestor became readable = %q, want %q: the stat error was memoized as an absence", got, want)
	}
}
