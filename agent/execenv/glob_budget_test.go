package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// stubMaxGlobDirListings lowers the directory-listing budget for a test and
// restores it when the test ends, so a budget test can use a tree small
// enough for t.TempDir() rather than one large enough to trip the real
// (100,000) bound.
func stubMaxGlobDirListings(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobDirListings
	maxGlobDirListings = n
	t.Cleanup(func() { maxGlobDirListings = orig })
}

// stubMaxGlobMatches lowers the match-count budget for a test and restores it
// when the test ends.
func stubMaxGlobMatches(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobMatches
	maxGlobMatches = n
	t.Cleanup(func() { maxGlobMatches = orig })
}

// globBudgetFixture builds a t.TempDir() tree of n sibling directories, each
// holding one leaf.txt and one leaf.md, so tests can glob for one extension,
// both extensions, or count total directories without rebuilding the tree.
func globBudgetFixture(t *testing.T, n int) (root string) {
	t.Helper()
	root = t.TempDir()
	for i := range n {
		dir := filepath.Join(root, fmt.Sprintf("dir%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "leaf.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "leaf.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobStopsAtTheDirectoryListingBudgetWithFileIdentity is the unit-scale
// reproduction of #497: maxUnidentifiedGlobDirs only ever counted directories
// whose FileInfo carries no file identity, but ext4/APFS/HFS+ give every
// directory (dev, ino) identity, so the bound was unreachable on a real
// filesystem. A `**` glob rooted at a huge real directory tree (like `/`) had
// termination (the ancestor/SameFile cycle check) but no bound on the work or
// memory it could spend getting there. This proves the bound now counts every
// listing, not only identity-less ones — using a real os-backed tree so the
// test cannot silently drift onto the identity-less arm the old bound covered.
func TestGlobStopsAtTheDirectoryListingBudgetWithFileIdentity(t *testing.T) {
	const dirCount = 40
	root := globBudgetFixture(t, dirCount)

	info, err := os.Stat(filepath.Join(root, "dir00"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFileIdentity(info) {
		t.Fatalf("a real directory must carry file identity, else this test exercises the wrong bound")
	}

	const budget = 8
	stubMaxGlobDirListings(t, budget)

	var counter *countingFS
	stubGlobBaseFS(t, func(dir string) fs.FS {
		counter = &countingFS{FS: os.DirFS(dir)}
		return counter
	})

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.txt", root, true)
	if err == nil {
		t.Fatalf("Glob over a %d-directory tree with a listing budget of %d returned no error and %d matches; the listing budget was not enforced", dirCount, budget, len(matches))
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("budget refusal reported %v, which the walk skips silently; it must fail the glob visibly instead", err)
	}
	if counter.calls > budget+1 {
		t.Fatalf("walk made %d directory listings against a budget of %d, want at most %d", counter.calls, budget, budget+1)
	}
	if counter.calls >= dirCount+1 {
		t.Fatalf("walk listed all %d directories instead of stopping early (%d listings made)", dirCount+1, counter.calls)
	}
}

// TestGlobTruncatesAtTheMatchCap proves globMatches's other missing bound: the
// GlobWalk callback appended every match to the result slice forever, so a
// `**` pattern with millions of hits accumulated all of them in memory before
// GlobWithExclusions ever got a chance to return. The cap must also stop the
// walk itself once it trips, not merely truncate the slice after a full walk
// completes — a truncate-after-the-fact fix would satisfy the match count but
// still pay the full listing cost, which is the resource the bug actually
// wastes. That is why this compares against an uncapped control run over the
// identical tree rather than a hand-picked listing count.
func TestGlobTruncatesAtTheMatchCap(t *testing.T) {
	const fileCount = 24
	root := globBudgetFixture(t, fileCount)

	var full *countingFS
	stubGlobBaseFS(t, func(dir string) fs.FS {
		full = &countingFS{FS: os.DirFS(dir)}
		return full
	})
	fullMatches, _, fullTruncatedAt, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.txt", root, true)
	if err != nil {
		t.Fatalf("uncapped control run: %v", err)
	}
	if len(fullMatches) != fileCount || fullTruncatedAt != 0 {
		t.Fatalf("uncapped control run = (%d matches, truncatedAt=%d), want (%d, 0)", len(fullMatches), fullTruncatedAt, fileCount)
	}

	stubMaxGlobMatches(t, 5)

	var capped *countingFS
	stubGlobBaseFS(t, func(dir string) fs.FS {
		capped = &countingFS{FS: os.DirFS(dir)}
		return capped
	})
	matches, _, truncatedAt, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.txt", root, true)
	if err != nil {
		t.Fatalf("capped run: %v", err)
	}
	if len(matches) != 5 {
		t.Fatalf("capped run returned %d matches, want exactly 5", len(matches))
	}
	if truncatedAt != 5 {
		t.Fatalf("capped run reported truncatedAt=%d, want 5", truncatedAt)
	}
	if capped.calls >= full.calls {
		t.Fatalf("capped run made %d directory listings, want fewer than the uncapped run's %d listings (the walk must stop once the match cap trips, not just truncate the result afterward)", capped.calls, full.calls)
	}
}

// TestGlobBudgetIsSharedAcrossBraceExpandedPatterns proves the budget has to
// live at the glob call, not at each expanded pattern:
// globpattern.Expand can turn one brace pattern into up to
// globpattern.MaxExpansions (256) separately-walked patterns, and globMatches
// runs a fresh globWalkFS per pattern. A budget scoped to that walk would let
// a 256-way brace expansion multiply the cap by 256 instead of bounding the
// call it belongs to. Two extensions stand in for two expansions here.
func TestGlobBudgetIsSharedAcrossBraceExpandedPatterns(t *testing.T) {
	const fileCount = 24
	root := globBudgetFixture(t, fileCount)

	stubMaxGlobMatches(t, 5)

	matches, _, truncatedAt, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.{txt,md}", root, true)
	if err != nil {
		t.Fatalf("GlobWithExclusions: %v", err)
	}
	if len(matches) > 5 {
		t.Fatalf("brace-expanded glob (two patterns) returned %d matches, want at most 5 shared across the whole call, not 5 per expanded pattern", len(matches))
	}
	if truncatedAt != 5 {
		t.Fatalf("brace-expanded glob reported truncatedAt=%d, want 5 (the call-wide cap)", truncatedAt)
	}
}

// TestGlobWalkRetainsIdentityOnlyForThePathBeingWalked covers the third
// defect behind #497: w.listed is a map keyed by every directory the walk has
// ever listed, and nothing ever removes an entry once admit adds it. The
// ancestor cycle check in admit only ever consults ancestors of the directory
// currently being admitted, so a wide, shallow tree (siblings, not nesting)
// should cost the walk O(1) retained identities, not one per directory
// visited. This builds a wide real tree, so hasFileIdentity is true throughout,
// and pins the failure via retained() rather than via a memory measurement
// that would be flaky to assert on directly.
func TestGlobWalkRetainsIdentityOnlyForThePathBeingWalked(t *testing.T) {
	const siblingCount = 30
	root := globBudgetFixture(t, siblingCount)

	w := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), budget: &globBudget{}}
	if _, err := w.ReadDir("."); err != nil {
		t.Fatalf("listing the root: %v", err)
	}
	for i := range siblingCount {
		dir := fmt.Sprintf("dir%02d", i)
		if _, err := w.ReadDir(dir); err != nil {
			t.Fatalf("listing %s: %v", dir, err)
		}
	}

	if retained := w.retained(); retained > 2 {
		t.Fatalf("walk retained %d directory identities after listing %d sibling directories; retained count grew with the number of directories listed, not with the depth of the path being walked (want at most 2: the root plus the sibling being listed)", retained, siblingCount)
	}

	// The cycle check must still work after whatever pruning made the count
	// above small — a fix that throws identity away entirely would pass the
	// retention assertion but silently reopen #369 (the `**` / never
	// terminating). A directory symlink back at the tree root must still be
	// refused as a readdir on an already-listed ancestor.
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skipf("directory symlinks unavailable on this platform: %v", err)
	}
	_, err := w.ReadDir("loop")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDir(loop) through a symlink back at the tree root = %v, want an fs.ErrNotExist PathError (the walk refusing an already-listed ancestor)", err)
	}
}
