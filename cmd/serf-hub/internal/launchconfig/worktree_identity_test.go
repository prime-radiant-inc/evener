package launchconfig

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

// TestPathsFor_MetaAndLegacyProjectKeyedByStableRoot proves the fix for the
// hub-side half of
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Active content root vs stable identity root"): trust metadata
// (Meta) and legacy project state (LegacyProject) must be keyed off the
// stable main-repo root so trust decisions survive switching between a repo's
// linked worktrees, while Repo/Project config content stays keyed off the
// active cwd (the checked-out worktree).
func TestPathsFor_MetaAndLegacyProjectKeyedByStableRoot(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	stateRoot := t.TempDir()

	mainPaths := PathsFor(stateRoot, main)
	wtPaths := PathsFor(stateRoot, wt)

	if mainPaths.Meta != wtPaths.Meta {
		t.Errorf("Meta path differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainPaths.Meta, wtPaths.Meta)
	}
	if mainPaths.LegacyProject != wtPaths.LegacyProject {
		t.Errorf("LegacyProject path differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainPaths.LegacyProject, wtPaths.LegacyProject)
	}
	// Active content paths must still track the per-worktree cwd, not the
	// stable root — a worktree's own .serf/launch.toml must be read, not the
	// main checkout's.
	if mainPaths.Repo == wtPaths.Repo {
		t.Errorf("Repo path must differ between main root and worktree (active content), got the same %q for both", mainPaths.Repo)
	}
	if mainPaths.Project == wtPaths.Project {
		t.Errorf("Project path must differ between main root and worktree (active content), got the same %q for both", mainPaths.Project)
	}
}

// TestResolve_TrustFromMainRootAppliesInLinkedWorktree drives the same
// scenario through the full Resolve() path: a repo layer trusted while
// launched from the main root must be honored when the identical content is
// resolved from a linked worktree of the same repo, because trust is TOFU'd
// against the content hash + stable identity, not the raw cwd.
func TestResolve_TrustFromMainRootAppliesInLinkedWorktree(t *testing.T) {
	main, wt := newLinkedWorktree(t)
	stateRoot := t.TempDir()

	raw := `skills_dirs = ["sub"]`
	// Plant the identical repo layer content in both the main checkout and
	// the linked worktree (as a real worktree checkout would have, since
	// they share tracked history) but only record a trust decision once,
	// via the paths computed from the main root.
	writeFile(t, filepath.Join(main, ".serf", "launch.toml"), raw)
	writeFile(t, filepath.Join(wt, ".serf", "launch.toml"), raw)

	hash, err := CanonicalHashTOML([]byte(raw))
	if err != nil {
		t.Fatalf("CanonicalHashTOML: %v", err)
	}
	mainPaths := PathsFor(stateRoot, main)
	writeFile(t, mainPaths.Meta, `schema = 1
cwd = "`+main+`"
[trust]
hash = "`+hash+`"
decision = "trusted"
`)

	got, err := Resolve(stateRoot, wt, Layer{})
	if err != nil {
		t.Fatalf("Resolve(wt): %v", err)
	}
	if got.Repo == nil || got.Repo.Trust != TrustTrusted {
		t.Fatalf("worktree repo trust = %v, want trusted (inherited from the main-root trust decision)", got.Repo)
	}
}
