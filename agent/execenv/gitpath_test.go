package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestGitRootOrEmptyLocalNonRepoDoesNotForkGit(t *testing.T) {
	nonRepo := t.TempDir()
	shimDir, count := gitCountingShim(t, "/should/not/be/used")
	t.Setenv("PATH", shimDir)

	if got := GitRootOrEmpty(NewLocalExecutionEnvironment(nonRepo), nonRepo); got != "" {
		t.Fatalf("non-repo cwd = %q, want empty", got)
	}
	if n := count(); n != 0 {
		t.Fatalf("GitRootOrEmpty forked git %d times for a local non-repo, want 0", n)
	}
}

// TestFreshGitRootRefusesAnOversizedPointer pins that the FRESH lookup reads a
// ".git" pointer with the pointer reader and keeps a refused read inconclusive.
// A pointer is one path line, so a file past the pointer cap is not a pointer;
// the padding follows a valid, parseable pointer line, so only the size cap can
// produce the refusal. That refusal is neither a repository root nor the
// absence of one, and it must not reach the git binary: the launcher searches a
// PATH the workspace can reach.
func TestFreshGitRootRefusesAnOversizedPointer(t *testing.T) {
	dir := t.TempDir()
	pointer := "gitdir: " + filepath.Join(dir, ".git", "worktrees", "lane") + "\n" +
		strings.Repeat("x", gitPointerMaxBytes)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(pointer), 0o600); err != nil {
		t.Fatalf("write .git pointer: %v", err)
	}
	// Not parallel: the PATH shim is process-wide.
	shimDir, count := gitCountingShim(t, t.TempDir())
	t.Setenv("PATH", shimDir)

	root, definitive := GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(dir), dir)
	if root != "" {
		t.Fatalf("fresh git root = %q, want empty: an oversized .git pointer is not a repository root", root)
	}
	if n := count(); n != 0 {
		t.Fatalf("fresh lookup forked git %d times, want 0: a refused pointer must not reach the binary", n)
	}
	if definitive {
		t.Fatal("oversized pointer produced a definitive answer: a refused read is unresolved, not a confirmed absence")
	}
}

// TestFreshGitRootFallsBackForAReadableNonPointer pins the other side of the
// refusal: a ".git" file that was READ successfully and simply is not a gitdir
// pointer is an ordinary structural miss, so the fresh lookup keeps its
// Git-binary fallback. Only a refused read closes that path.
func TestFreshGitRootFallsBackForAReadableNonPointer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir pointer\n"), 0o600); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	// Not parallel: the PATH shim is process-wide.
	shimDir, count := gitCountingShim(t, dir)
	t.Setenv("PATH", shimDir)

	root, definitive := GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(dir), dir)
	if n := count(); n != 1 {
		t.Fatalf("fresh lookup forked git %d times, want 1: a readable non-pointer keeps the binary fallback", n)
	}
	if want := evalSym(t, dir); root != want {
		t.Fatalf("fresh git root = %q, want %q", root, want)
	}
	if !definitive {
		t.Fatal("git answered the fallback, so the result must be definitive")
	}
}

// TestFreshGitRootRefusesAnUnresolvableEntry pins that a ".git" entry whose
// stat fails for a reason other than absence is refused by the same walk that
// looks for a root. Walking past it would let the directory inherit an
// enclosing repository's root, and a second probe would let the workspace plant
// an entry between the two walks for the Git binary to see but never read.
func TestFreshGitRootRefusesAnUnresolvableEntry(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	work := filepath.Join(repo, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("make work: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("make enclosing .git: %v", err)
	}
	// A self-referential ".git" symlink: Stat fails with ELOOP, not ENOENT, so
	// the entry exists but cannot be resolved.
	if err := os.Symlink(".git", filepath.Join(work, ".git")); err != nil {
		t.Fatalf("make .git symlink loop: %v", err)
	}
	// Not parallel: the PATH shim is process-wide.
	shimDir, count := gitCountingShim(t, repo)
	t.Setenv("PATH", shimDir)

	root, definitive := GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(work), work)
	if root != "" {
		t.Fatalf("fresh git root = %q, want empty: an unresolvable .git entry must not inherit the enclosing root", root)
	}
	if n := count(); n != 0 {
		t.Fatalf("fresh lookup forked git %d times, want 0: an unresolvable entry must not reach the binary", n)
	}
	if definitive {
		t.Fatal("unresolvable .git entry produced a definitive answer: it is unresolved, not an absence")
	}
}

// TestFreshGitRootRefusesADanglingGitSymlink pins the entry shape Stat cannot
// tell from absence: a ".git" symlink whose target is missing. The link is a
// marker the directory carries, so the walk must not step past it — not to an
// enclosing checkout, and not to a definitive absence it would memoize.
func TestFreshGitRootRefusesADanglingGitSymlink(t *testing.T) {
	// Not parallel: the PATH shim is process-wide.
	shimDir, count := gitCountingShim(t, "/should/not/be/used")
	t.Setenv("PATH", shimDir)

	t.Run("does not inherit an enclosing checkout", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "repo")
		work := filepath.Join(repo, "work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatalf("make work: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatalf("make enclosing .git: %v", err)
		}
		if err := os.Symlink(filepath.Join(work, "missing"), filepath.Join(work, ".git")); err != nil {
			t.Fatalf("make dangling .git symlink: %v", err)
		}

		root, definitive := GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(work), work)
		if root != "" {
			t.Fatalf("fresh git root = %q, want empty: a dangling .git symlink must not inherit the enclosing root", root)
		}
		if n := count(); n != 0 {
			t.Fatalf("fresh lookup forked git %d times, want 0: a dangling entry must not reach the binary", n)
		}
		if definitive {
			t.Fatal("dangling .git symlink produced a definitive answer: it is unresolved, not an absence")
		}
	})

	t.Run("does not record an absence", func(t *testing.T) {
		work := t.TempDir()
		if err := os.Symlink(filepath.Join(work, "missing"), filepath.Join(work, ".git")); err != nil {
			t.Fatalf("make dangling .git symlink: %v", err)
		}

		root, definitive := GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(work), work)
		if root != "" {
			t.Fatalf("fresh git root = %q, want empty", root)
		}
		if definitive {
			t.Fatal("dangling .git symlink produced a definitive absence: the question is unresolved")
		}
	})
}
