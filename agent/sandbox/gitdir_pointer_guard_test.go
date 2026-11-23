package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oversizedGitPointerContent returns a pointer file that PARSES as a valid
// linked-worktree pointer but occupies more than the 4 KiB a pointer can hold.
// The trailing spaces are ignored by the pointer parser, so only the size cap
// can reject it: a reader that loads it whole classifies the worktree.
func oversizedGitPointerContent(target string) []byte {
	return []byte("gitdir: " + target + "\n" + strings.Repeat(" ", 8*1024))
}

// TestClassifyWorkspaceRefusesOversizedGitPointer pins that sandbox policy
// classification does not load a workspace-controlled .git file whole.
func TestClassifyWorkspaceRefusesOversizedGitPointer(t *testing.T) {
	main := clean(t.TempDir())
	target := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(worktree, ".git"), oversizedGitPointerContent(target), 0o600); err != nil {
		t.Fatal(err)
	}

	layout, err := ClassifyWorkspaceWithCeilings(worktree, nil)
	if err == nil {
		t.Fatalf("classified a .git pointer larger than any pointer: %+v", layout)
	}
}
