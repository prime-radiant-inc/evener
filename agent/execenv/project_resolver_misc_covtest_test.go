package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocalStructuralMainRoot_GitDir covers the .git directory happy path
// (line 124): a directory with a .git subdirectory resolves to itself.
func TestLocalStructuralMainRoot_GitDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, isGit, handled, err := localStructuralMainRoot(dir)
	if err != nil {
		t.Fatalf("localStructuralMainRoot: %v", err)
	}
	if !isGit || !handled {
		t.Errorf("isGit=%v handled=%v, want both true", isGit, handled)
	}
	if root == "" {
		t.Error("expected non-empty root")
	}
}

// TestLocalStructuralMainRoot_NoGit covers the no-git-ancestor path (line
// 146-147): a directory with no .git anywhere returns handled=true, isGit=false.
func TestLocalStructuralMainRoot_NoGit(t *testing.T) {
	dir := t.TempDir()
	root, isGit, handled, err := localStructuralMainRoot(dir)
	if err != nil {
		t.Fatalf("localStructuralMainRoot: %v", err)
	}
	if isGit {
		t.Error("expected isGit=false for directory with no .git")
	}
	if !handled {
		t.Error("expected handled=true")
	}
	if root != "" {
		t.Errorf("expected empty root, got %q", root)
	}
}

// TestLocalStructuralMainRoot_MalformedGitPointer covers the malformed Git
// worktree pointer path (line 137-138): a .git file with content that is not a
// valid gitdir pointer returns an error.
func TestLocalStructuralMainRoot_MalformedGitPointer(t *testing.T) {
	dir := t.TempDir()
	// Write a .git file with invalid content.
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir pointer"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, isGit, handled, err := localStructuralMainRoot(dir)
	if err == nil {
		t.Fatal("expected error for malformed Git pointer")
	}
	if !isGit || !handled {
		t.Errorf("isGit=%v handled=%v, want both true", isGit, handled)
	}
}

// TestLocalStructuralMainRoot_ReadGitFileError covers the ReadFile error path
// (line 127-128): a .git file that cannot be read returns an error.
func TestLocalStructuralMainRoot_ReadGitFileError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, cannot test permission errors")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /somewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove read permission.
	if err := os.Chmod(gitFile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitFile, 0o644) })
	_, _, _, err := localStructuralMainRoot(dir)
	if err == nil {
		t.Fatal("expected read error for unreadable .git file")
	}
}
