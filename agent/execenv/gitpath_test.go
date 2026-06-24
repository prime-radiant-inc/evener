package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

// TestGitRootOrEmptyMemoizedPerEnv proves the per-environment cache: a second
// lookup returns the first result without re-resolving (so it survives the .git
// dir being removed under it), while a fresh environment re-resolves and sees
// the repo is gone.
func TestGitRootOrEmptyMemoizedPerEnv(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	// EvalSymlinks the dir so the expected root matches GitRootOrEmpty's own
	// symlink resolution (macOS /var -> /private/var, /tmp on some systems).
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	env := NewLocalExecutionEnvironment(dir)
	root := GitRootOrEmpty(env, dir)
	if root != resolved {
		t.Fatalf("git root = %q, want %q", root, resolved)
	}

	// Remove the repo. An uncached lookup would now return "".
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}

	if got := GitRootOrEmpty(env, dir); got != root {
		t.Fatalf("cached lookup after .git removal = %q, want cached %q", got, root)
	}
	fresh := NewLocalExecutionEnvironment(dir)
	if got := GitRootOrEmpty(fresh, dir); got != "" {
		t.Fatalf("fresh env after .git removal = %q, want empty (re-resolved)", got)
	}
}

// TestGitRootOrEmptyCacheKeyedByCwd proves distinct working dirs do not collide
// in the cache.
func TestGitRootOrEmptyCacheKeyedByCwd(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	nonRepo := t.TempDir() // separate temp dir, not a repo

	env := NewLocalExecutionEnvironment(repo)
	if got := GitRootOrEmpty(env, repo); got != resolvedRepo {
		t.Fatalf("repo cwd = %q, want %q", got, resolvedRepo)
	}
	if got := GitRootOrEmpty(env, nonRepo); got != "" {
		t.Fatalf("non-repo cwd = %q, want empty (distinct cache key)", got)
	}
	// Repo lookup is unaffected by the non-repo lookup.
	if got := GitRootOrEmpty(env, repo); got != resolvedRepo {
		t.Fatalf("repo cwd after non-repo lookup = %q, want %q", got, resolvedRepo)
	}
}
