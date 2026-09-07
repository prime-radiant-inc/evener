package execenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"primeradiant.com/evener/agent/sandbox"
)

// stubMaxGlobDirListings lowers the directory-listing budget for a test and
// restores it when the test ends, so a budget test can use a tree small
// enough for t.TempDir() rather than one large enough to trip the real bound.
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

// stubMaxGlobDirEntries lowers the per-directory entry budget for a test and
// restores it when the test ends, so a budget test can use a directory small
// enough for t.TempDir() rather than one large enough to trip the real bound.
func stubMaxGlobDirEntries(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobDirEntries
	maxGlobDirEntries = n
	t.Cleanup(func() { maxGlobDirEntries = orig })
}

// stubMaxGlobLiveEntries lowers the call-wide live-entry budget for a test
// and restores it when the test ends, so a budget test can use a tree small
// enough for t.TempDir() rather than one large enough to trip the real bound.
func stubMaxGlobLiveEntries(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobLiveEntries
	maxGlobLiveEntries = n
	t.Cleanup(func() { maxGlobLiveEntries = orig })
}

// stubGlobDirChunk shrinks the per-syscall chunk size for a test and restores
// it when the test ends, so a bounded listing has to make several chunk reads
// over a fixture small enough for t.TempDir() instead of a directory too
// large to build here.
func stubGlobDirChunk(t *testing.T, n int) {
	t.Helper()
	orig := globDirChunk
	globDirChunk = n
	t.Cleanup(func() { globDirChunk = orig })
}

// pacedDirEntriesFS wraps a directory so a test can observe how many entries
// a chunked reader actually pulls from it. Its files hand back at most pace
// entries per ReadDir(n) call once the caller asks for more than that,
// mirroring the short reads a real filesystem can hand a chunked reader, so a
// small fixture can still force several round trips instead of resolving in
// the one big read a directory too large to build here would need. A caller
// that asks for everything at once (n <= 0 — what an unbounded listing does)
// still gets the whole directory back in a single call, which is what makes
// this double also show that today's listing pulls everything at once.
//
// When cancel is set, a file also cancels on its cancelOn'th ReadDir(n) call —
// the same shape countingFS uses for a whole directory listing, scoped down to
// the chunk calls inside one — so a test can watch how much of a listing a
// chunk loop keeps pulling after the context that should have stopped it is
// cancelled.
type pacedDirEntriesFS struct {
	fs.FS
	read     *int
	pace     int
	cancelOn int
	cancel   context.CancelFunc
}

func (p pacedDirEntriesFS) Open(name string) (fs.File, error) {
	f, err := p.FS.Open(name)
	if err != nil {
		return nil, err
	}
	rdf, ok := f.(fs.ReadDirFile)
	if !ok {
		return f, nil
	}
	return &pacedDirEntriesFile{ReadDirFile: rdf, read: p.read, pace: p.pace, cancelOn: p.cancelOn, cancel: p.cancel}, nil
}

type pacedDirEntriesFile struct {
	fs.ReadDirFile
	read     *int
	pace     int
	cancelOn int
	cancel   context.CancelFunc
	calls    int
}

func (p *pacedDirEntriesFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if n > 0 && n > p.pace {
		n = p.pace
	}
	entries, err := p.ReadDirFile.ReadDir(n)
	*p.read += len(entries)
	p.calls++
	if p.cancel != nil && p.calls == p.cancelOn {
		p.cancel()
	}
	return entries, err
}

// flatEntriesFixture builds a t.TempDir() holding n files and no
// subdirectories, so a test can force one directory listing to read past the
// per-directory entry budget without building a tree.
func flatEntriesFixture(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := range n {
		p := filepath.Join(root, fmt.Sprintf("leaf%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
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
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		counter = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}}
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
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		full = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}}
		return full
	})
	fullBudget := NewGlobBudget()
	fullMatches, _, err := NewLocalExecutionEnvironment(root).GlobWithBudget(t.Context(), "**/*.txt", root, true, fullBudget)
	if err != nil {
		t.Fatalf("uncapped control run: %v", err)
	}
	if len(fullMatches) != fileCount || fullBudget.TruncatedAt() != 0 {
		t.Fatalf("uncapped control run = (%d matches, truncatedAt=%d), want (%d, 0)", len(fullMatches), fullBudget.TruncatedAt(), fileCount)
	}

	stubMaxGlobMatches(t, 5)

	var capped *countingFS
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		capped = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}}
		return capped
	})
	budget := NewGlobBudget()
	matches, _, err := NewLocalExecutionEnvironment(root).GlobWithBudget(t.Context(), "**/*.txt", root, true, budget)
	if err != nil {
		t.Fatalf("capped run: %v", err)
	}
	if len(matches) != 5 {
		t.Fatalf("capped run returned %d matches, want exactly 5", len(matches))
	}
	if budget.TruncatedAt() != 5 {
		t.Fatalf("capped run reported truncatedAt=%d, want 5", budget.TruncatedAt())
	}
	if capped.calls >= full.calls {
		t.Fatalf("capped run made %d directory listings, want fewer than the uncapped run's %d listings (the walk must stop once the match cap trips, not just truncate the result afterward)", capped.calls, full.calls)
	}
}

// TestGlobWithExclusionsRefusesRatherThanTruncatingSilently proves requirement
// (c): a caller with no budget of its own has no way to learn a result was
// truncated, so GlobWithExclusions must refuse outright — a non-nil error —
// rather than hand back a plausible-looking short list with a nil error.
func TestGlobWithExclusionsRefusesRatherThanTruncatingSilently(t *testing.T) {
	const fileCount = 24
	root := globBudgetFixture(t, fileCount)

	stubMaxGlobMatches(t, 5)

	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.txt", root, true)
	if err == nil {
		t.Fatalf("GlobWithExclusions over a %d-match tree with a cap of 5 returned (%v, %d, nil); a budget-less caller has no way to learn the list was truncated, so it must refuse instead", fileCount, matches, excluded)
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

	budget := NewGlobBudget()
	matches, _, err := NewLocalExecutionEnvironment(root).GlobWithBudget(t.Context(), "**/*.{txt,md}", root, true, budget)
	if err != nil {
		t.Fatalf("GlobWithBudget: %v", err)
	}
	if len(matches) > 5 {
		t.Fatalf("brace-expanded glob (two patterns) returned %d matches, want at most 5 shared across the whole call, not 5 per expanded pattern", len(matches))
	}
	if budget.TruncatedAt() != 5 {
		t.Fatalf("brace-expanded glob reported truncatedAt=%d, want 5 (the call-wide cap)", budget.TruncatedAt())
	}
}

// TestGlobStopsOnADirectoryWithTooManyEntries is the unit-scale reproduction
// of #497's other half (roborev High): the listing budget and match cap only
// ever get a say once a directory's ReadDir call returns, and os.DirFS's
// ReadDir is os.ReadDir, which reads every entry before handing any of them
// back — so one directory with millions of entries can exhaust memory before
// either bound is ever consulted. pacedDirEntriesFS paces what a chunked
// reader gets per call, the same short-read shape a real filesystem can hand
// back, so a small fixture can still show a listing stopping after a few
// chunks instead of needing a directory too large to build here. The read
// counter and peakDirEntries are what tell a fix that stops early apart from
// one that reads the whole directory and only then reports the refusal — a
// result-only assertion cannot tell the two apart, but the OOM only the
// former avoids.
func TestGlobStopsOnADirectoryWithTooManyEntries(t *testing.T) {
	const fileCount = 30
	root := flatEntriesFixture(t, fileCount)

	const budget = 10
	stubMaxGlobDirEntries(t, budget)

	var read int
	var seenBudget *GlobBudget
	stubGlobBaseFS(t, func(ctx context.Context, dir string, callBudget *GlobBudget) fs.FS {
		seenBudget = callBudget
		return boundedDirFS{FS: pacedDirEntriesFS{FS: os.DirFS(dir), read: &read, pace: 5}, budget: callBudget, ctx: ctx}
	})

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "*.txt", root, true)
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Glob over a %d-entry directory with an entry budget of %d = (%v, %v), want a *globBudgetError; nothing bounds how many entries one listing may materialize", fileCount, budget, matches, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "glob" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "glob")
	}
	if read > fileCount {
		t.Fatalf("paced double reported %d entries read out of %d total in the directory, which is impossible", read, fileCount)
	}
	if read == fileCount {
		t.Fatalf("listing read all %d entries before refusing; it must stop near the entry budget of %d instead of materializing the whole directory", read, budget)
	}
	if seenBudget.peakDirEntries < budget || seenBudget.peakDirEntries >= fileCount {
		t.Fatalf("globBudget.peakDirEntries = %d, want at least the entry budget of %d but strictly less than the directory's %d entries (a listing that materializes everything before refusing must not pass this)", seenBudget.peakDirEntries, budget, fileCount)
	}
}

// TestGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree proves the
// call-wide live-entry bound catches what maxGlobDirEntries cannot: a walk
// holds every ancestor directory's listing alive while it descends into a
// child, so a deep tree's peak live total is the per-directory entry count
// times its depth, not any single listing's size. Every directory in the
// chain here holds fewer entries than a lowered maxGlobDirEntries, so the
// per-directory cap never trips on its own, but the sum the walk is holding
// live grows with every level it descends and crosses a lowered
// maxGlobLiveEntries partway down. peakLiveEntries has to be checked
// directly, not just the refusal, because a fix that walked the whole tree
// and only complained at the end would return the same error this test's
// errors.As check accepts; comparing what was actually held live against the
// tree's full entry count is what tells the two apart.
func TestGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree(t *testing.T) {
	root := t.TempDir()

	const depth = 10    // d00..d09
	const perLevel = 5  // every directory in the chain holds this many entries
	const decoySize = 8 // files in a directory outside the chain
	const perDirBudget = 20
	const liveBudget = 30

	cur := root
	for i := range depth {
		cur = filepath.Join(cur, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(cur, 0o755); err != nil {
			t.Fatal(err)
		}
		// Every non-leaf directory holds perLevel-1 padding files plus the
		// subdirectory that continues the chain; the leaf holds perLevel
		// padding files and no subdirectory, so every level's own listing is
		// the same size.
		padding := perLevel - 1
		if i == depth-1 {
			padding = perLevel
		}
		for p := range padding {
			if err := os.WriteFile(filepath.Join(cur, fmt.Sprintf("pad%02d.txt", p)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// zzz_decoy sorts after the whole d00..d09 chain, so the walk finishes
	// descending and backing out of the chain before it ever lists this
	// directory: it contributes to the tree's total entry count but is never
	// one of the chain's ancestors, so it can never be live at the same time
	// as the chain's peak.
	decoy := filepath.Join(root, "zzz_decoy")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range decoySize {
		if err := os.WriteFile(filepath.Join(decoy, fmt.Sprintf("leaf%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Total entries in the tree: root's own listing (d00 + zzz_decoy = 2),
	// plus the chain (depth*perLevel), plus the decoy's own files.
	totalEntries := 2 + depth*perLevel + decoySize

	stubMaxGlobDirEntries(t, perDirBudget)
	stubMaxGlobLiveEntries(t, liveBudget)

	var seenBudget *GlobBudget
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		seenBudget = budget
		return boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}
	})

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.txt", root, true)
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Glob over a %d-level tree (peak live so far: %d) with a live-entry budget of %d = (%d matches, %v), want a *globBudgetError; nothing bounds how many entries a walk may hold live across the listings it still has open", depth, seenBudget.peakLiveEntries, liveBudget, len(matches), err)
	}
	if budgetErr.kind != budgetLiveEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetLiveEntries", budgetErr.kind)
	}
	if seenBudget.peakLiveEntries < liveBudget {
		t.Fatalf("globBudget.peakLiveEntries = %d, want at least the live-entry budget of %d", seenBudget.peakLiveEntries, liveBudget)
	}
	if seenBudget.peakLiveEntries >= totalEntries {
		t.Fatalf("globBudget.peakLiveEntries = %d, want strictly less than the tree's %d total entries (a fix that walked the whole tree before complaining must not pass this)", seenBudget.peakLiveEntries, totalEntries)
	}
}

// TestGlobSucceedsOnAWideShallowTreeUnderTheLiveEntryCeiling is the control
// for TestGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree above: it
// holds the same 60 total entries (10 sibling directories of 5 files each,
// plus the root's own 10-entry listing), but spread across siblings instead
// of nested, so the walk only ever holds the root's listing plus whichever
// one sibling it is currently reading — one directory's worth plus the root —
// no matter how many siblings it has already finished with. The live-entry
// budget is lowered to the same value the nested test uses, and the call
// still has to succeed and report every match: a naive cumulative counter
// that summed every listing ever made instead of releasing the ones the walk
// has left would grow with every sibling visited and refuse partway through
// this tree instead, which would break every large flat repository.
func TestGlobSucceedsOnAWideShallowTreeUnderTheLiveEntryCeiling(t *testing.T) {
	root := t.TempDir()

	const siblings = 10
	const filesPerSibling = 5
	const perDirBudget = 20
	const liveBudget = 30

	for i := range siblings {
		dir := filepath.Join(root, fmt.Sprintf("sib%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := range filesPerSibling {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("leaf%02d.txt", f)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	stubMaxGlobDirEntries(t, perDirBudget)
	stubMaxGlobLiveEntries(t, liveBudget)

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.txt", root, true)
	if err != nil {
		t.Fatalf("Glob over a %d-directory wide tree (same %d total entries as the nested tree above) under a live-entry budget of %d = %v, want nil error; the bound must charge only what the walk is currently holding live, not a running total across the whole call", siblings, siblings*(filesPerSibling+1), liveBudget, err)
	}
	if want := siblings * filesPerSibling; len(matches) != want {
		t.Fatalf("Glob over the wide tree returned %d matches, want %d", len(matches), want)
	}
}

// TestGlobMatchesStartsItsLiveEntryAccountingFresh pins the other half of the
// scoping rule: that globMatches actually applies it. Each expanded pattern
// is its own traversal, and the ignore-discovery pass before them is another,
// so a walk has to begin holding nothing. This hands globMatches a budget
// that is already holding a root listing, as a finished earlier traversal
// would leave it, and asks for a pattern whose own listing fits the ceiling
// comfortably on its own.
func TestGlobMatchesStartsItsLiveEntryAccountingFresh(t *testing.T) {
	const ceiling = 10
	const staleRootEntries = 8
	stubMaxGlobLiveEntries(t, ceiling)

	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(nested, fmt.Sprintf("leaf%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	budget := newGlobBudget("glob")
	if err := budget.holdEntries(".", staleRootEntries); err != nil {
		t.Fatalf("seeding a finished traversal's %d held entries: %v", staleRootEntries, err)
	}

	ctx := t.Context()
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: ctx}
	matches, err := globMatches(ctx, fsys, "a/b/*.txt", budget)
	if err != nil {
		t.Fatalf("globMatches(a/b/*.txt) = %v, want no refusal: an earlier traversal's %d held entries are no longer live and must not be counted against the ceiling of %d", err, staleRootEntries, ceiling)
	}
	if len(matches) != 3 {
		t.Fatalf("globMatches(a/b/*.txt) returned %d matches, want 3", len(matches))
	}
}

// TestGlobLiveEntryTrackingIsScopedToOneTraversal proves the live-entry
// ceiling is bookkeeping about ONE traversal rather than about the budget
// object, which several traversals of a single call share. holdEntries keeps
// every entry that is an ancestor of the directory being listed, which is
// right inside a traversal — those listings really are still held — and wrong
// across two, because a directory the previous traversal was holding when it
// finished is not held any more. Root is an ancestor of everything, so
// without a reset it survives forever and is counted against every later
// traversal that starts beneath it.
//
// This drives the budget directly rather than through a glob call, so it
// pins the rule itself instead of whatever order a particular call happens to
// visit directories in.
func TestGlobLiveEntryTrackingIsScopedToOneTraversal(t *testing.T) {
	const ceiling = 10
	const rootEntries = 8
	const nestedEntries = 5
	stubMaxGlobLiveEntries(t, ceiling)

	// Within one traversal an ancestor's listing is still held, so its
	// entries count toward the ceiling alongside the directory below it.
	within := newGlobBudget("glob")
	if err := within.holdEntries(".", rootEntries); err != nil {
		t.Fatalf("holding %d entries under the root, within the ceiling of %d: %v", rootEntries, ceiling, err)
	}
	if err := within.holdEntries("a/b", nestedEntries); err == nil {
		t.Fatalf("holding %d entries under a/b while the root's %d are still held stayed within the ceiling of %d, want a refusal: an ancestor's listing is still live and has to be counted", nestedEntries, rootEntries, ceiling)
	}

	// Once a traversal ends, what it was holding is no longer held, so the
	// next traversal starts from nothing even where it shares ancestors.
	across := newGlobBudget("glob")
	if err := across.holdEntries(".", rootEntries); err != nil {
		t.Fatalf("first traversal holding %d entries under the root: %v", rootEntries, err)
	}
	across.resetLive()
	if err := across.holdEntries("a/b", nestedEntries); err != nil {
		t.Fatalf("second traversal holding %d entries under a/b = %v, want no refusal: the first traversal's root listing is no longer held and must not be counted against a ceiling of %d", nestedEntries, err, ceiling)
	}
}

// TestGlobStopsReadingAChunkedListingOnCancellation proves the other half of
// reading a directory in chunks: readDirChunked's loop checks ctx between one
// chunk and the next, so a cancellation landing mid-listing is noticed within
// about one more chunk of work rather than after the whole directory has been
// pulled through. cancelFS only checks ctx once, when a listing starts, and
// the walk callback that finally sees ctx.Err() only runs after ReadDir
// returns, so the call reports context.Canceled either way regardless of how
// much of the directory the loop actually read — a test that checked only
// the returned error would pass even if the loop read every remaining chunk
// before giving up. What has to be pinned is the chunk loop itself stopping
// promptly, which is why this also counts how many entries actually came out
// of the directory file, the same way pacedDirEntriesFS's read counter does
// for TestGlobStopsOnADirectoryWithTooManyEntries above. Losing the ctx check
// between chunks would let the loop keep pulling chunks until the directory
// is exhausted — up to maxGlobDirEntries/globDirChunk chunks of pointless
// work after the caller asked to stop — while still reporting the same
// context.Canceled this test's error check alone cannot tell apart from that.
func TestGlobStopsReadingAChunkedListingOnCancellation(t *testing.T) {
	const fileCount = 40
	const chunkSize = 5
	root := flatEntriesFixture(t, fileCount)
	stubGlobDirChunk(t, chunkSize)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var read int
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		fsys := pacedDirEntriesFS{FS: os.DirFS(dir), read: &read, pace: chunkSize, cancelOn: 2, cancel: cancel}
		return boundedDirFS{FS: fsys, budget: budget, ctx: ctx}
	})

	matches, err := NewLocalExecutionEnvironment(root).Glob(ctx, "*.txt", root, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Glob over a listing cancelled mid-chunk = (%v, %v), want context.Canceled", matches, err)
	}
	if want := chunkSize * 3; read > want {
		t.Fatalf("listing kept reading after cancellation: read %d of %d entries in the directory, want at most %d (about one chunk past the one that observed the cancellation)", read, fileCount, want)
	}
}

// retained reports how many directory identities this walk is holding, which
// is the walk's own memory cost: the cycle check consults only the ancestors
// of the directory it is admitting, so this must track the depth of the path
// being walked, not the number of directories the walk has ever listed.
func (w *globWalkFS) retained() int { return len(w.chain) }

// TestGlobWalkRetainsIdentityOnlyForThePathBeingWalked pins the walk's own
// memory cost, the third defect behind #497. The ancestor cycle check in
// admit consults only the ancestors of the directory it is admitting, so what
// the walk holds has to scale with the depth of the path it is on and not
// with how many directories it has listed in total. A wide, shallow tree
// (siblings, not nesting) separates the two: 30 siblings are 30 listings at
// depth one. It builds a real tree so hasFileIdentity is true throughout, and
// reads retained() rather than measuring memory, which would be flaky to
// assert on directly.
func TestGlobWalkRetainsIdentityOnlyForThePathBeingWalked(t *testing.T) {
	const siblingCount = 30
	root := globBudgetFixture(t, siblingCount)

	w := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), budget: newGlobBudget("glob")}
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

// TestGlobWalkRefusesACycleThroughAnUnlistedAncestor pins the ancestor check
// for the exact shape doublestar creates when a pattern's meta-free prefix
// names a path directly: the very first listing the walk ever makes can be
// several levels below the root, so neither the root (".") nor the path's own
// parent has ever been pushed onto the chain. identity's fallback (a fresh
// fs.Stat on a chain miss) has to find the cycle anyway, by walking up to an
// ancestor it has never listed and stat'ing it fresh.
//
// This is a characterization pin, not a red test: it already passes on the
// current tree. It would fail if identity's chain-miss fallback were replaced
// by an always-false lookup, since then nothing would catch the cycle before
// the walk ever lists "." or "a".
func TestGlobWalkRefusesACycleThroughAnUnlistedAncestor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil {
		t.Skipf("directory symlinks unavailable on this platform: %v", err)
	}

	w := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), budget: newGlobBudget("glob")}
	_, err := w.ReadDir("a/loop")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDir(a/loop) as the walk's first-ever listing = %v, want an fs.ErrNotExist PathError (the ancestor cycle back to root, caught via a fresh stat rather than the chain)", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Fatalf("ReadDir(a/loop) = %v (%T), want an *fs.PathError", err, err)
	}
}

// TestGlobBudgetErrorRecordsWhetherTheWalkCouldDetectCycles asserts the
// budget refusal structurally rather than by matching its prose: a caller
// (or a test) has to be able to tell "this filesystem could never have
// detected a symlink cycle" from "this really is an enormous tree" without
// parsing a sentence. cycleSafe carries that distinction — false for an
// identity-less fstest.MapFS, which can never rule out a cycle, true for an
// os.DirFS tree, which can — and budget records the bound that was crossed.
func TestGlobBudgetErrorRecordsWhetherTheWalkCouldDetectCycles(t *testing.T) {
	stubMaxGlobDirListings(t, 1)

	mapTree := fstest.MapFS{"dir00/leaf.txt": &fstest.MapFile{Data: []byte("x")}}
	mw := &globWalkFS{FS: mapTree, ctx: t.Context(), budget: newGlobBudget("glob")}
	if _, err := mw.ReadDir("."); err != nil {
		t.Fatalf("listing the MapFS root: %v", err)
	}
	_, err := mw.ReadDir("dir00")
	var mapErr *globBudgetError
	if !errors.As(err, &mapErr) {
		t.Fatalf("tripping the budget over an identity-less MapFS = %v (%T), want a *globBudgetError", err, err)
	}
	if mapErr.cycleSafe {
		t.Fatalf("globBudgetError.cycleSafe = true for an identity-less filesystem that can never detect a cycle, want false")
	}
	if mapErr.budget != maxGlobDirListings {
		t.Fatalf("globBudgetError.budget = %d, want %d (the active budget)", mapErr.budget, maxGlobDirListings)
	}

	root := globBudgetFixture(t, 1)
	ow := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), budget: newGlobBudget("glob")}
	if _, err := ow.ReadDir("."); err != nil {
		t.Fatalf("listing the os.DirFS root: %v", err)
	}
	_, err = ow.ReadDir("dir00")
	var osErr *globBudgetError
	if !errors.As(err, &osErr) {
		t.Fatalf("tripping the budget over an os.DirFS tree = %v (%T), want a *globBudgetError", err, err)
	}
	if !osErr.cycleSafe {
		t.Fatalf("globBudgetError.cycleSafe = false for an os-backed filesystem that can detect a cycle, want true")
	}
	if osErr.budget != maxGlobDirListings {
		t.Fatalf("globBudgetError.budget = %d, want %d (the active budget)", osErr.budget, maxGlobDirListings)
	}
}

// TestSandboxedGlobTruncatesToAStablePrefix proves the sandboxed walk's match
// cap truncates to a deterministic prefix rather than to whichever entries
// the filesystem's raw directory order happened to hand back first.
// secureDirFS.ReadDir sorts its entries lexically after reading a directory,
// so which files survive a cap tripping mid-listing is the glob's business,
// not an accident of the fd-backed listing's own order (which has no
// ordering guarantee of its own to begin with). Every file is written in
// reverse-lexical order and given an identical mtime, so neither creation
// order nor modification time can accidentally line up with the alphabetical
// order the sort guarantees, and two back-to-back runs over the unchanged
// tree must agree with each other and with that order. An unsorted ReadDir
// would let the raw fd order decide which files survive truncation instead,
// breaking the fixture's guarantee that both runs and the lexically-first
// "want" prefix agree with each other: that is the only way this test can
// fail.
func TestSandboxedGlobTruncatesToAStablePrefix(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)

	const fileCount = 12
	fixedMod := time.Now()
	for i := fileCount - 1; i >= 0; i-- {
		p := filepath.Join(worktree, fmt.Sprintf("file%02d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, fixedMod, fixedMod); err != nil {
			t.Fatal(err)
		}
	}

	stubMaxGlobMatches(t, 4)

	want := []string{
		filepath.Join(worktree, "file00.txt"),
		filepath.Join(worktree, "file01.txt"),
		filepath.Join(worktree, "file02.txt"),
		filepath.Join(worktree, "file03.txt"),
	}

	firstBudget := NewGlobBudget()
	first, _, err := env.GlobWithBudget(t.Context(), "*.txt", worktree, true, firstBudget)
	if err != nil {
		t.Fatalf("first sandboxed glob: %v", err)
	}
	if firstBudget.TruncatedAt() != 4 {
		t.Fatalf("first run truncatedAt = %d, want 4", firstBudget.TruncatedAt())
	}
	second, _, err := env.GlobWithBudget(t.Context(), "*.txt", worktree, true, NewGlobBudget())
	if err != nil {
		t.Fatalf("second sandboxed glob: %v", err)
	}

	if !slices.Equal(first, second) {
		t.Fatalf("two sandboxed globs over the same unchanged tree returned different truncated prefixes: %v vs %v", first, second)
	}
	if !slices.Equal(first, want) {
		t.Fatalf("sandboxed glob truncated to %v, want the lexically first 4: %v", first, want)
	}
}

// TestGlobMatchesReportsCancellationAfterTheCapTripped proves globMatches's
// cap fast path does not shadow a cancellation that landed at the same
// moment: budget.full() returning early must still check ctx first, or a
// call cancelled right after the cap tripped comes back reporting truncated
// success instead of the cancellation the caller actually asked for.
func TestGlobMatchesReportsCancellationAfterTheCapTripped(t *testing.T) {
	budget := newGlobBudget("glob")
	budget.truncated = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	matches, err := globMatches(ctx, fstest.MapFS{}, "*.txt", budget)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globMatches with a tripped cap and a cancelled context = (%v, %v), want context.Canceled", matches, err)
	}
}

// TestGlobChargesThePatternWalkOnTheDefaultIgnoreExclusionPath covers the
// arm the tool actually calls: include_ignored defaults to false, so a
// .gitignore scan runs over the base before the pattern walk starts. That
// scan is a separate traversal with its own bound (see the stacked PR that
// budgets it); what this pins is that its presence does not stop the pattern
// walk's own listings from being charged, so the default path is bounded
// rather than silently exempt.
func TestGlobChargesThePatternWalkOnTheDefaultIgnoreExclusionPath(t *testing.T) {
	const dirCount = 40
	const budget = 8
	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, budget)

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.txt", root, false)
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("Glob(include_ignored=false) over a %d-directory tree with a listing budget of %d = (%d matches, %v), want a *globBudgetError; the pattern walk's listings are not being charged on the default path", dirCount, budget, len(matches), err)
	}
	if budgetErr.count <= budget {
		t.Fatalf("globBudgetError.count = %d, want more than the budget of %d", budgetErr.count, budget)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("budget refusal reported %v, which the walk skips silently; it must fail the glob visibly instead", err)
	}
}

// TestGlobIgnoreDiscoveryRefusesRatherThanUnderExcluding pins that a budget
// refusal during .gitignore discovery reaches the caller instead of being
// skipped like an unreadable entry. Discovery reads the same filesystem the
// pattern walk does, under the same bounds, so one can trip while it is
// collecting rules. An ignoreSet that gave up partway still reports itself
// complete, so every rule it never reached silently stops excluding: the call
// then returns paths it was asked to leave out. That is a wrong answer rather
// than a missing one, and it is worse than failing.
//
// The oversized directory here carries the .gitignore that would exclude the
// candidate, so swallowing the refusal is directly observable: the glob comes
// back with a path the rules cover.
func TestGlobIgnoreDiscoveryRefusesRatherThanUnderExcluding(t *testing.T) {
	const fileCount = 30
	const entryCap = 10

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range fileCount {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("bulk%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobDirEntries(t, entryCap)

	matches, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "secret.txt", root, false)
	if err == nil {
		t.Fatalf("Glob(\"secret.txt\") = (%v, nil), want a refusal: discovery gave up on the entry cap before reading the .gitignore that excludes secret.txt, so the rules were never loaded and the path came back anyway", matches)
	}
	if _, refused := errors.AsType[*globBudgetError](err); !refused {
		t.Fatalf("Glob(\"secret.txt\") = %v (%T), want a *globBudgetError", err, err)
	}
}

// TestBoundedReadRefusesMidListingOnTheLiveEntryCeiling pins that the
// live-entry ceiling is consulted while a directory is being read, not once
// the read is done. The total it bounds already includes every ancestor
// listing the walk is holding, so a directory whose own entries sit well
// under the per-directory cap can still cross the ceiling partway through its
// own read. Charging only at the end would let the peak reach the ceiling
// plus a whole directory cap before anything refused, which is a ceiling only
// in retrospect.
//
// The assertion is on entries actually held at the moment of refusal, because
// the returned error is identical either way: what distinguishes the two is
// how much memory was committed before it arrived.
func TestBoundedReadRefusesMidListingOnTheLiveEntryCeiling(t *testing.T) {
	const ancestorHeld = 15
	const ceiling = 20
	const dirEntries = 10
	const chunk = 2

	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range dirEntries {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stubMaxGlobLiveEntries(t, ceiling)
	stubGlobDirChunk(t, chunk)
	// High enough that the per-directory cap never fires: the only bound in
	// play here is the aggregate one.
	stubMaxGlobDirEntries(t, 1000)

	budget := newGlobBudget("glob")
	if err := budget.holdEntries(".", ancestorHeld); err != nil {
		t.Fatalf("seeding the ancestor's %d held entries: %v", ancestorHeld, err)
	}

	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	entries, err := fsys.ReadDir("sub")
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("ReadDir(sub) with %d entries already held against a ceiling of %d = (%d entries, %v), want a *globBudgetError", ancestorHeld, ceiling, len(entries), err)
	}
	if budgetErr.kind != budgetLiveEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetLiveEntries", budgetErr.kind)
	}
	// Refusing mid-read means the peak sits just past the ceiling, within one
	// chunk of it. Refusing after the read would put it at the ancestor's
	// holdings plus the directory's whole contents.
	if budgetErr.count > ceiling+chunk {
		t.Fatalf("refused holding %d entries, want no more than the ceiling of %d plus one chunk of %d: the read ran to the end before the ceiling was consulted", budgetErr.count, ceiling, chunk)
	}
	if budget.peakLiveEntries >= ancestorHeld+dirEntries {
		t.Fatalf("peakLiveEntries = %d, want less than the %d the whole directory would add to the ancestor's holdings: the ceiling was checked only after the read finished", budget.peakLiveEntries, ancestorHeld+dirEntries)
	}
}
