package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// seedListDirTree writes n plainly-named files into dir, so a listing has a
// deterministic sorted prefix f00, f01, ...
func seedListDirTree(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A small page must bound the walk, not the render: with a budget sized for a
// one-entry page (offset+limit+1 == 2), the unconfined walk must read the
// directory once, retain only the two entries it needs, and report truncation
// instead of enumerating the whole directory.
func TestListDirectoryBudget_EntryBudgetBoundsWalk(t *testing.T) {
	dir := t.TempDir()
	seedListDirTree(t, dir, 10)
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	full, err := env.ListDirectory("", 1)
	if err != nil {
		t.Fatalf("baseline ListDirectory: %v", err)
	}
	if len(full) != 10 {
		t.Fatalf("baseline ListDirectory returned %d entries, want 10", len(full))
	}

	orig := listReadDir
	calls := 0
	listReadDir = func(name string) ([]os.DirEntry, error) {
		calls++
		return orig(name)
	}
	defer func() { listReadDir = orig }()

	budget := NewListDirBudget(2)
	got, err := env.ListDirectoryBudget(context.Background(), "", 1, budget)
	if err != nil {
		t.Fatalf("ListDirectoryBudget: %v", err)
	}
	if !budget.Truncated() {
		t.Fatal("walk over a 10-entry directory with a 2-entry budget did not report truncation")
	}
	if len(got) != 2 {
		t.Fatalf("budgeted walk returned %d entries, want exactly the 2-entry budget", len(got))
	}
	if calls != 1 {
		t.Fatalf("budgeted walk made %d directory listings, want 1", calls)
	}
	if got[0].Name != "f00" || got[1].Name != "f01" {
		t.Fatalf("budgeted walk returned %+v, want the sorted prefix f00,f01", got)
	}
}

// Cancelling ctx mid-walk must abort the unconfined traversal.
func TestListDirectoryBudget_CancelsWalk(t *testing.T) {
	dir := t.TempDir()
	seedListDirTree(t, dir, 10)
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	ctx, cancel := context.WithCancel(context.Background())
	orig := listReadDir
	listReadDir = func(name string) ([]os.DirEntry, error) {
		cancel() // cancel during the walk's first listing
		return orig(name)
	}
	defer func() { listReadDir = orig }()

	if _, err := env.ListDirectoryBudget(ctx, "", 1, &ListDirBudget{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled walk error = %v, want context.Canceled", err)
	}
}

// An unbounded budget is exactly ListDirectory: the empty budget must not change
// the entries or their order for callers that want the whole subtree.
func TestListDirectoryBudget_UnboundedMatchesListDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedListDirTree(t, dir, 3)
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	full, err := env.ListDirectory("", 2)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	got, err := env.ListDirectoryBudget(context.Background(), "", 2, &ListDirBudget{})
	if err != nil {
		t.Fatalf("ListDirectoryBudget: %v", err)
	}
	if !reflect.DeepEqual(full, got) {
		t.Fatalf("unbounded budget diverged from ListDirectory:\nfull=%+v\ngot =%+v", full, got)
	}
}
