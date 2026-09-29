package execenv

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	orig := listOpenDir
	calls := 0
	listOpenDir = func(name string) (*os.File, error) {
		calls++
		return orig(name)
	}
	defer func() { listOpenDir = orig }()

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
	orig := listOpenDir
	listOpenDir = func(name string) (*os.File, error) {
		cancel() // cancel during the walk's first listing
		return orig(name)
	}
	defer func() { listOpenDir = orig }()

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

// The listing bound must stop the recursion, not only the entries: with a
// listing budget that runs out during a depth-2 walk, the walk stops there
// instead of descending into every subdirectory.
func TestListDirectoryBudget_ListingBudgetBoundsWalk(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("d%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	// The top directory is listing 1; its first two children are listings 2 and
	// 3, so the third child's recursion is refused and the walk stops after the
	// top directory's first three entries.
	budget := &ListDirBudget{maxListings: 3}
	got, err := env.ListDirectoryBudget(context.Background(), "", 2, budget)
	if err != nil {
		t.Fatalf("ListDirectoryBudget: %v", err)
	}
	if !budget.Truncated() {
		t.Fatal("walk with a 3-listing budget did not report truncation")
	}
	if len(got) != 3 {
		t.Fatalf("listing-bound walk returned %d entries, want 3 (the top directory's first three before recursion was refused)", len(got))
	}
}

// dirNames returns the entries' names in order, for asserting a listing.
func dirNames(entries []DirEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

// A chunked listing must return the true lexically smallest prefix when it has
// to merge several non-empty read batches and replace a held entry with a
// smaller one arriving later. The read hands entries back in a deliberately
// wrong order, in batches small enough that the heap must merge across chunks.
func TestListDirectoryBudget_SelectsSortedPrefix(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "bravo", "delta", "mike", "zeta"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	byName := map[string]os.DirEntry{}
	all, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		byName[e.Name()] = e
	}
	batch := func(names ...string) []os.DirEntry {
		out := make([]os.DirEntry, len(names))
		for i, n := range names {
			out[i] = byName[n]
		}
		return out
	}

	restoreChunk := listDirChunk
	listDirChunk = 2
	defer func() { listDirChunk = restoreChunk }()
	origRead := listReadDirChunk
	defer func() { listReadDirChunk = origRead }()
	// serve hands back the given batches in order, then EOF.
	serve := func(batches ...[]os.DirEntry) {
		i := 0
		listReadDirChunk = func(*os.File, int) ([]os.DirEntry, error) {
			if i >= len(batches) {
				return nil, io.EOF
			}
			b := batches[i]
			i++
			return b, nil
		}
	}

	// Unsorted batches, two entries at a time, so cross-chunk merging and heap
	// replacement both run.
	t.Run("merges out-of-order chunks", func(t *testing.T) {
		serve(
			batch("zeta", "mike"),
			batch("delta", "bravo"),
			batch("alpha"),
		)
		budget := NewListDirBudget(10)
		got, err := env.ListDirectoryBudget(context.Background(), "", 1, budget)
		if err != nil {
			t.Fatalf("ListDirectoryBudget: %v", err)
		}
		if want := []string{"alpha", "bravo", "delta", "mike", "zeta"}; !reflect.DeepEqual(dirNames(got), want) {
			t.Fatalf("listing = %v, want ascending %v", dirNames(got), want)
		}
	})

	t.Run("replaces held entries with smaller ones", func(t *testing.T) {
		serve(
			batch("zeta", "mike"),
			batch("delta", "bravo"),
			batch("alpha"),
		)
		budget := NewListDirBudget(3)
		got, err := env.ListDirectoryBudget(context.Background(), "", 1, budget)
		if err != nil {
			t.Fatalf("ListDirectoryBudget: %v", err)
		}
		if !budget.Truncated() {
			t.Fatal("truncated read did not report truncation")
		}
		if want := []string{"alpha", "bravo", "delta"}; !reflect.DeepEqual(dirNames(got), want) {
			t.Fatalf("listing = %v, want the three smallest %v", dirNames(got), want)
		}
	})
}

// A finite page must not drive an unbounded scan of a pathological directory:
// the scan cap stops the chunked read and reports the listing incomplete.
func TestListDirectoryBudget_ScanCapBoundsScan(t *testing.T) {
	dir := t.TempDir()
	seedListDirTree(t, dir, 10)
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	restoreChunk := listDirChunk
	listDirChunk = 2
	defer func() { listDirChunk = restoreChunk }()
	restoreCap := maxListDirScanEntries
	maxListDirScanEntries = 3
	defer func() { maxListDirScanEntries = restoreCap }()

	origRead := listReadDirChunk
	scanned := 0
	listReadDirChunk = func(f *os.File, n int) ([]os.DirEntry, error) {
		batch, rerr := origRead(f, n)
		scanned += len(batch)
		return batch, rerr
	}
	defer func() { listReadDirChunk = origRead }()

	budget := NewListDirBudget(100)
	got, err := env.ListDirectoryBudget(context.Background(), "", 1, budget)
	if err != nil {
		t.Fatalf("ListDirectoryBudget: %v", err)
	}
	if !budget.Truncated() {
		t.Fatal("scan-capped walk did not report truncation")
	}
	if scanned >= 10 {
		t.Fatalf("scan-capped walk read the whole 10-entry directory despite a %d-entry cap", maxListDirScanEntries)
	}
	if scanned > maxListDirScanEntries+listDirChunk {
		t.Fatalf("scan-capped walk read %d entries, want at most the cap %d plus one chunk", scanned, maxListDirScanEntries)
	}
	if len(got) == 0 {
		t.Fatal("scan-capped walk returned no entries though the directory had some")
	}
}
