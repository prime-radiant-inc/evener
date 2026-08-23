package agent

import (
	"testing"
)

// TestIsWorktreeListPreserving_EmptyArgs covers the len(args)==0 path
// (line 94) which returns true (no command, no mutation).
func TestIsWorktreeListPreserving_EmptyArgs(t *testing.T) {
	t.Parallel()
	if !isWorktreeListPreserving(nil) {
		t.Fatal("empty args should be preserving")
	}
	if !isWorktreeListPreserving([]string{}) {
		t.Fatal("empty args should be preserving")
	}
}

// TestIsWorktreeListPreserving_SymbolicRefRead covers the symbolic-ref
// read form with at most one non-flag arg (line 104, countNonFlagArgs <= 1).
func TestIsWorktreeListPreserving_SymbolicRefRead(t *testing.T) {
	t.Parallel()
	if !isWorktreeListPreserving([]string{"symbolic-ref", "HEAD"}) {
		t.Fatal("symbolic-ref HEAD should be preserving (read)")
	}
	if !isWorktreeListPreserving([]string{"symbolic-ref", "--short", "HEAD"}) {
		t.Fatal("symbolic-ref --short HEAD should be preserving (read)")
	}
}

// TestIsWorktreeListPreserving_SymbolicRefWrite covers the symbolic-ref
// write form with more than one non-flag arg (line 104, countNonFlagArgs > 1).
func TestIsWorktreeListPreserving_SymbolicRefWrite(t *testing.T) {
	t.Parallel()
	if isWorktreeListPreserving([]string{"symbolic-ref", "HEAD", "refs/heads/x"}) {
		t.Fatal("symbolic-ref HEAD refs/heads/x should NOT be preserving (write)")
	}
	if isWorktreeListPreserving([]string{"symbolic-ref", "-q", "HEAD", "refs/heads/x"}) {
		t.Fatal("symbolic-ref -q HEAD refs/heads/x should NOT be preserving (write)")
	}
}

// TestIsWorktreeListPreserving_WorktreeNonList covers the worktree subcommand
// that is NOT list (line 100, args[1] != "list" returns false).
func TestIsWorktreeListPreserving_WorktreeNonList(t *testing.T) {
	t.Parallel()
	if isWorktreeListPreserving([]string{"worktree", "remove", "lane"}) {
		t.Fatal("worktree remove should NOT be preserving")
	}
	if isWorktreeListPreserving([]string{"worktree"}) {
		t.Fatal("worktree with no subcommand should NOT be preserving")
	}
}

// TestIsWorktreeListPreserving_ReadOnlyCommand covers the read-only command
// map lookup (line 106).
func TestIsWorktreeListPreserving_ReadOnlyCommand(t *testing.T) {
	t.Parallel()
	for cmd := range worktreeReadOnlyGitCommands {
		if !isWorktreeListPreserving([]string{cmd, "arg1"}) {
			t.Errorf("read-only command %q should be preserving", cmd)
		}
	}
	if isWorktreeListPreserving([]string{"checkout", "main"}) {
		t.Fatal("checkout should NOT be preserving (not in read-only map)")
	}
}

// TestCountNonFlagArgs covers countNonFlagArgs (lines 110-116) including
// flags, non-flags, and mixed.
func TestCountNonFlagArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"empty", nil, 0},
		{"all flags", []string{"-q", "--quiet"}, 0},
		{"all non-flags", []string{"HEAD", "refs/heads/x"}, 2},
		{"mixed", []string{"-q", "HEAD", "refs/heads/x"}, 2},
		{"single flag", []string{"-"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countNonFlagArgs(tc.args); got != tc.want {
				t.Fatalf("countNonFlagArgs(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
