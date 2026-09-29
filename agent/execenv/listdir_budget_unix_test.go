//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// The confined (fd-anchored) walk must be bounded the same way as the
// unconfined one: a budget sized for a one-entry page reads the directory once
// and retains only the entries it needs.
func TestListDirectoryBudget_ConfinedEntryBudgetBoundsWalk(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	seedListDirTree(t, worktree, 10)

	orig := secureReadDirEntries
	calls := 0
	secureReadDirEntries = func(fd int) ([]os.DirEntry, error) {
		calls++
		return orig(fd)
	}
	defer func() { secureReadDirEntries = orig }()

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
	if calls != 1 {
		t.Fatalf("confined budgeted walk made %d directory listings, want 1", calls)
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
	orig := secureReadDirEntries
	secureReadDirEntries = func(fd int) ([]os.DirEntry, error) {
		cancel() // cancel during the walk's first listing
		return orig(fd)
	}
	defer func() { secureReadDirEntries = orig }()

	if _, err := env.ListDirectoryBudget(ctx, "", 1, &ListDirBudget{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("confined cancelled walk error = %v, want context.Canceled", err)
	}
}
