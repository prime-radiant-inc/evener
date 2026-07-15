package cmdutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

// runGit runs a git command in dir with a fixed identity, failing the test on
// error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newLinkedWorktree builds an origin-less main repo with one commit and a
// linked worktree, returning their absolute paths.
func newLinkedWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	runGit(t, base, "init", "-q", "main")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(base, "wt")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	return main, wt
}

// TestDefaultProjectStateDir_LinkedWorktreeSameAsMain proves the fix for the
// bug described in
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Runtime state keying at launch"): for an origin-less repo, RuntimeDir
// used to key off the raw workDir, so launching from a linked worktree
// computed a different project state dir than launching from the main repo
// root. DefaultProjectStateDir must key both the same.
func TestDefaultProjectStateDir_LinkedWorktreeSameAsMain(t *testing.T) {
	main, wt := newLinkedWorktree(t)

	_, mainDir, err := DefaultProjectStateDir(main)
	if err != nil {
		t.Fatal(err)
	}
	_, wtDir, err := DefaultProjectStateDir(wt)
	if err != nil {
		t.Fatal(err)
	}

	if mainDir != wtDir {
		t.Errorf("state dir differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainDir, wtDir)
	}
}

func TestDefaultProjectStateDir_ClonesWithSameOriginDiffer(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGit(t, base, "init", "-q", "--bare", remote)
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	runGit(t, base, "clone", "-q", remote, first)
	runGit(t, base, "clone", "-q", remote, second)

	_, firstDir, err := DefaultProjectStateDir(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondDir, err := DefaultProjectStateDir(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDir == secondDir {
		t.Fatalf("same-origin clones share project state dir: %q", firstDir)
	}
}

// TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir covers the
// not-in-a-repo case: ResolveMainRepoRootLocal returns "" and the state dir
// must key off workDir unchanged, matching pre-existing behavior for
// non-git directories.
func TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir(t *testing.T) {
	workDir := t.TempDir()

	_, got, err := DefaultProjectStateDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	_, want, err := DefaultProjectStateDir(workDir) // deterministic given the same input
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Errorf("DefaultProjectStateDir(%q) not deterministic: %q vs %q", workDir, got, want)
	}
	// The state dir must be keyed by workDir itself (no git ancestor to
	// resolve), i.e. it must differ across two distinct non-repo dirs.
	other := t.TempDir()
	_, otherDir, err := DefaultProjectStateDir(other)
	if err != nil {
		t.Fatal(err)
	}
	if got == otherDir {
		t.Errorf("DefaultProjectStateDir collided for distinct non-repo workDirs %q and %q: %q", workDir, other, got)
	}
}

func TestResolveStateKeyDir_NonGitSymlinkUsesSharedCanonicalPath(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveStateKeyDir(alias); got != project.CanonicalPath {
		t.Fatalf("ResolveStateKeyDir(%q) = %q, want shared canonical path %q", alias, got, project.CanonicalPath)
	}
}
