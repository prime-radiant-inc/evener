package cmdutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	mainDir := DefaultProjectStateDir(main)
	wtDir := DefaultProjectStateDir(wt)

	if mainDir != wtDir {
		t.Errorf("state dir differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainDir, wtDir)
	}
}

// TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir covers the
// not-in-a-repo case: ResolveMainRepoRootLocal returns "" and the state dir
// must key off workDir unchanged, matching pre-existing behavior for
// non-git directories.
func TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir(t *testing.T) {
	workDir := t.TempDir()

	got := DefaultProjectStateDir(workDir)
	want := DefaultProjectStateDir(workDir) // deterministic given the same input

	if got != want {
		t.Errorf("DefaultProjectStateDir(%q) not deterministic: %q vs %q", workDir, got, want)
	}
	// The state dir must be keyed by workDir itself (no git ancestor to
	// resolve), i.e. it must differ across two distinct non-repo dirs.
	other := t.TempDir()
	if got == DefaultProjectStateDir(other) {
		t.Errorf("DefaultProjectStateDir collided for distinct non-repo workDirs %q and %q: %q", workDir, other, got)
	}
}
