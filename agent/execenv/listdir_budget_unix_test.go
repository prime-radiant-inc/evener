//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// The confined (fd-anchored) walk must be bounded the same way as the
// unconfined one: a budget sized for a one-entry page dups the directory once,
// retains only the entries it needs, and reports truncation rather than reading
// the whole directory to EOF.
func TestListDirectoryBudget_ConfinedEntryBudgetBoundsWalk(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	seedListDirTree(t, worktree, 10)

	orig := secureDupDirFd
	listings := 0
	secureDupDirFd = func(fd int) (int, error) {
		listings++
		return orig(fd)
	}
	defer func() { secureDupDirFd = orig }()

	budget := NewListDirBudget(2)
	got, err := env.ListDirectoryBudget(context.Background(), "", 1, budget)
	if err != nil {
		t.Fatalf("confined ListDirectoryBudget: %v", err)
	}
	if !budget.Truncated() {
		t.Fatal("confined walk over a 10-entry directory with a 2-entry budget did not report truncation")
	}
	if len(got) != 2 {
		t.Fatalf("confined budgeted walk returned %d entries, want exactly the 2-entry budget", len(got))
	}
	if listings != 1 {
		t.Fatalf("confined budgeted walk made %d directory listings, want 1", listings)
	}
	if got[0].Name != "f00" || got[1].Name != "f01" {
		t.Fatalf("confined budgeted walk returned %+v, want the sorted prefix f00,f01", got)
	}
}

// Cancelling ctx mid-walk must abort the confined traversal too.
func TestListDirectoryBudget_ConfinedCancelsWalk(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	seedListDirTree(t, worktree, 10)

	ctx, cancel := context.WithCancel(context.Background())
	orig := secureDupDirFd
	secureDupDirFd = func(fd int) (int, error) {
		cancel() // cancel during the walk's first listing
		return orig(fd)
	}
	defer func() { secureDupDirFd = orig }()

	if _, err := env.ListDirectoryBudget(ctx, "", 1, &ListDirBudget{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("confined cancelled walk error = %v, want context.Canceled", err)
	}
}
