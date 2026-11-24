package identifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oversizedPointerContent returns a pointer file that PARSES as a valid gitdir
// pointer but occupies more than the 4 KiB a pointer can hold. Trailing spaces
// on a second line are ignored by ParseGitdirPointer, so the only thing that
// can reject this file is the size cap: a reader that loads it whole answers
// with the checkout it names.
func oversizedPointerContent(target string) []byte {
	return []byte("gitdir: " + target + "\n" + strings.Repeat(" ", 8*1024))
}

// TestGitEntryResolvesToCommonRefusesOversizedPointer pins that the
// main-checkout check does not load a workspace-controlled .git file whole.
func TestGitEntryResolvesToCommonRefusesOversizedPointer(t *testing.T) {
	candidate := t.TempDir()
	common := filepath.Join(t.TempDir(), ".git")
	mustDir(t, filepath.Join(common, "worktrees", "id"))
	if err := os.WriteFile(filepath.Join(candidate, ".git"), oversizedPointerContent(filepath.Join(common, "worktrees", "id")), 0o600); err != nil {
		t.Fatal(err)
	}

	if GitEntryResolvesToCommon(candidate, common) {
		t.Fatal("a .git pointer larger than any pointer resolved to a common dir; it must be refused unread")
	}
}

// TestMainCheckoutLocalRefusesOversizedPointer pins that resolving a project's
// identity refuses the same oversized pointer instead of reading it whole.
func TestMainCheckoutLocalRefusesOversizedPointer(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	target := filepath.Join(main, ".git", "worktrees", "wt")
	mustDir(t, target)
	worktree := filepath.Join(dir, "wt")
	mustDir(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, ".git"), oversizedPointerContent(target), 0o600); err != nil {
		t.Fatal(err)
	}

	root, isGit, err := mainCheckoutLocal(worktree)
	if err == nil {
		t.Fatalf("mainCheckoutLocal accepted a .git pointer larger than any pointer: root=%q isGit=%v", root, isGit)
	}
	if !isGit {
		t.Fatal("an oversized .git pointer must be reported as a Git entry that could not be resolved, not as absence")
	}
}
