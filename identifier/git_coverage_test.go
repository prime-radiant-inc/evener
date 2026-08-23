package identifier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGitEntryResolvesToCommon_ReadError covers the branch where a `.git` file
// can be stat'd but not read (permissions changed between stat and read).
func TestGitEntryResolvesToCommon_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /some/path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gitFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitFile, 0o644) })
	if GitEntryResolvesToCommon(dir, "/some/path") {
		t.Fatal("expected false when .git file cannot be read")
	}
}

// TestGitEntryResolvesToCommon_MalformedPointer covers the branch where the
// .git file exists and is readable, but its content is not a valid gitdir pointer.
func TestGitEntryResolvesToCommon_MalformedPointer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if GitEntryResolvesToCommon(dir, "/some/path") {
		t.Fatal("expected false for a non-pointer .git file")
	}
}

// TestGitEntryResolvesToCommon_RelativeGitdir covers the branch where the
// gitdir pointer is relative, so it is joined with the candidate directory.
func TestGitEntryResolvesToCommon_RelativeGitdir(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, "common", ".git")
	candidate := filepath.Join(root, "candidate")
	wtDir := filepath.Join(common, "worktrees", "wt")
	mustDir(t, wtDir)
	mustDir(t, candidate)
	rel, err := filepath.Rel(candidate, wtDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, ".git"), []byte("gitdir: "+rel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GitEntryResolvesToCommon(candidate, common) {
		t.Fatal("relative gitdir pointer should resolve to common")
	}
}

// TestMainCheckoutLocal_ReadError covers the branch in mainCheckoutLocal where
// a .git file exists but cannot be read.
func TestMainCheckoutLocal_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /some/path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gitFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitFile, 0o644) })
	_, _, err := mainCheckoutLocal(dir)
	if err == nil {
		t.Fatal("expected error when .git file cannot be read")
	}
}

// TestMainCheckoutLocal_SubmodulePointer_NonGitDir covers gitBinaryMainRootLocal
// line 196-198: a submodule-shaped .git pointer (target under .git/modules/) in
// a directory that is NOT a real git checkout, so `git rev-parse
// --git-common-dir` fails and returns an error.
func TestMainCheckoutLocal_SubmodulePointer_NonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	// Create a submodule-shaped target: .../parent/.git/modules/sub
	target := filepath.Join(dir, "parent", ".git", "modules", "sub")
	mustDir(t, target)
	work := filepath.Join(dir, "work")
	mustDir(t, work)
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: " + target + "\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// validateSubmodulePointer passes (submodule shape), so we reach
	// gitBinaryMainRootLocal. Since "work" is not a git repo, git rev-parse
	// --git-common-dir fails → line 196-198.
	_, _, err := mainCheckoutLocal(work)
	if err == nil {
		t.Fatal("expected error when git rev-parse fails in a non-git directory")
	}
}

// TestMainCheckoutLocal_SubmodulePointer_RealGitRepo covers gitBinaryMainRootLocal
// lines 199-206: a submodule-shaped .git pointer in a real git checkout where
// git rev-parse --git-common-dir succeeds. The candidate (parent of the common
// dir) does not match (no .git at the candidate path pointing to common), so
// it falls through to --show-toplevel. The bare modules repo has no work tree,
// so --show-toplevel fails, covering line 204-206.
func TestMainCheckoutLocal_SubmodulePointer_RealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := mustMkdir(t, filepath.Join(t.TempDir(), "repo"))
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")

	// Create a submodule-shaped target: a bare git repo inside .git/modules/sub.
	modulesDir := filepath.Join(root, ".git", "modules", "sub")
	mustDir(t, modulesDir)
	runGit(t, modulesDir, "init", "--bare", ".")
	submoduleWork := mustMkdir(t, filepath.Join(root, "submodule-work"))
	if err := os.WriteFile(filepath.Join(submoduleWork, ".git"), []byte("gitdir: " + modulesDir + "\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// validateSubmodulePointer passes (shape check), then gitBinaryMainRootLocal
	// runs git rev-parse --git-common-dir (succeeds, covering line 195) and
	// --show-toplevel (fails on the bare repo, covering line 204-206).
	_, _, err := mainCheckoutLocal(submoduleWork)
	if err == nil {
		t.Fatal("expected error when --show-toplevel fails on a bare modules repo")
	}
}

// TestMainCheckoutLocal_RealGitRepo covers the gitBinaryMainRootLocal path
// where git rev-parse succeeds. This exercises the common-dir resolution,
// candidate check, and the toplevel fallback.
func TestMainCheckoutLocal_RealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := mustMkdir(t, filepath.Join(t.TempDir(), "repo"))
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "t@t")
	runGit(t, root, "config", "user.name", "t")

	gotRoot, isGit, err := mainCheckoutLocal(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isGit {
		t.Fatal("expected isGit=true for a real git repo")
	}
	// mainCheckoutLocal returns the main checkout root. For a non-worktree
	// repo, that's the repo directory itself (possibly resolved through git
	// rev-parse --show-toplevel).
	if gotRoot == "" {
		t.Fatal("expected non-empty root")
	}
}

// TestValidateLinkedWorktreePointer_Malformed covers the branch where
// pointerTarget returns false (malformed pointer content).
func TestValidateLinkedWorktreePointer_Malformed(t *testing.T) {
	err := validateLinkedWorktreePointer("not a pointer", "/ancestor", "/root")
	if err == nil {
		t.Fatal("expected error for malformed worktree pointer")
	}
}

// TestValidateSubmodulePointer_Malformed covers the branch where pointerTarget
// returns false (malformed pointer content).
func TestValidateSubmodulePointer_Malformed(t *testing.T) {
	err := validateSubmodulePointer("not a pointer", "/ancestor")
	if err == nil {
		t.Fatal("expected error for malformed submodule pointer")
	}
}

// TestRunGitCmd_Failure covers the branch in runGitCmd where the git command
// fails (non-zero exit).
func TestRunGitCmd_Failure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Run a git command that will fail (rev-parse in a non-git directory).
	_, ok := runGitCmd(context.Background(), t.TempDir(), "rev-parse", "--git-common-dir")
	if ok {
		t.Fatal("expected runGitCmd to return ok=false in a non-git directory")
	}
}
