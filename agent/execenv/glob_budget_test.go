package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestGlobIgnoreDiscoveryRefusesRatherThanUnderExcluding pins the same
// contract for the whole-call listing budget, and names the consequence that
// makes it matter: the rules discovery did not reach are rules that stop
// excluding, so swallowing the refusal turns a bound into a wrong answer. A
// caller asked for paths to be left out and gets them back.
//
// Driven directly for the reason the entries test above gives: discovery and
// the pattern walk now cover the same ground, so only the unit boundary can
// say which of them reported the refusal.
func TestGlobIgnoreDiscoveryRefusesRatherThanUnderExcluding(t *testing.T) {
	const dirCount = 30
	const listingBudget = 5

	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, listingBudget)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over a %d-directory tree with a listing budget of %d = (%d rules, %v), want a *globBudgetError; a set that gave up partway would be reported as complete and would silently stop excluding", dirCount, listingBudget, len(set.dirs), err)
	}
	if budgetErr.kind != budgetListings {
		t.Fatalf("globBudgetError.kind = %v, want budgetListings", budgetErr.kind)
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

// TestGlobIgnoreDiscoveryIsChargedToTheBudget proves loadIgnoreSet's own walk
// spends the same budget the pattern walk does, rather than making a pass over
// the tree that no bound accounts for.
//
// Discovery's reach now matches the pattern's, so a pattern that lists nothing
// no longer isolates it. What still does is the include_ignored control: with
// it set, discovery is skipped entirely and only the pattern walk's listings
// are charged, so a budget that fits one pass but not two separates them. The
// recursive pattern is what makes discovery descend at all.
func TestGlobIgnoreDiscoveryIsChargedToTheBudget(t *testing.T) {
	const dirCount = 40
	// One pass over this tree is the base plus its directories; two passes
	// exceed this budget, one is comfortably inside it.
	const budget = dirCount + 20
	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, budget)

	var counter *countingFS
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		counter = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}}
		return counter
	})

	matches, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.txt", root, false)
	if err == nil {
		t.Fatalf("GlobWithExclusions(include_ignored=false) over a %d-directory tree with a listing budget of %d returned no error and %d matches; ignore discovery's own pass is not charged to the budget", dirCount, budget, len(matches))
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("budget refusal reported %v, which the walk skips silently; it must fail the glob visibly instead", err)
	}
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("GlobWithExclusions(include_ignored=false) error = %v (%T), want a *globBudgetError", err, err)
	}
	if budgetErr.op != "glob" {
		t.Fatalf("globBudgetError.op = %q, want %q (this is glob's own call, not grep's)", budgetErr.op, "glob")
	}
	if counter.calls > budget+1 {
		t.Fatalf("the call made %d directory listings against a budget of %d, want at most %d: it kept walking past the bound", counter.calls, budget, budget+1)
	}

	if _, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "does-not-exist.txt", root, true); err != nil {
		t.Fatalf("GlobWithExclusions(include_ignored=true) = %v, want nil (ignore discovery is skipped entirely, so it cannot trip the budget)", err)
	}
}

// TestGlobIgnoreDiscoveryStopsOnADirectoryWithTooManyEntries pins that a
// per-directory entry refusal raised while discovery is collecting rules
// reaches the caller instead of being skipped like an unreadable entry. An
// ignoreSet that gave up partway still reports itself complete, so every rule
// it never reached silently stops excluding and the call returns paths it was
// asked to leave out.
//
// It drives loadIgnoreSet directly. Discovery's reach is deliberately the same
// as the pattern's now, so through a glob call the pattern's own walk refuses
// on the same directory whether or not discovery propagated, and the outcomes
// are indistinguishable. What is being pinned is which pass reports it.
func TestGlobIgnoreDiscoveryStopsOnADirectoryWithTooManyEntries(t *testing.T) {
	const fileCount = 30
	const entryCap = 10
	root := flatEntriesFixture(t, fileCount)
	stubMaxGlobDirEntries(t, entryCap)
	stubGlobDirChunk(t, 5)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	_, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over a %d-entry directory with an entry budget of %d = %v, want a *globBudgetError; the refusal is being swallowed as if it were an unreadable entry", fileCount, entryCap, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "glob" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "glob")
	}
}

// TestGrepIgnoreDiscoveryIsChargedToTheBudget proves the same ignore-discovery
// budget applies on grep's native fallback, not only glob's: grepNative loads
// its own ignore set with a fresh budget before it ever walks the tree it
// greps, so an over-budget tree must fail the grep call, not silently glob
// past its bound. grepNative is called directly, rather than through a tool
// dispatch, because that is the "return \"\", err" path a native grep takes
// when loadIgnoreSet refuses — existing tests such as
// TestGrep_FallbackWithoutRipgrep reach the same native arm the same way,
// without needing to defeat ripgrep detection. The error must name "grep" as
// the operation that spent the budget: the budget is shared code with glob's,
// so nothing about the failure itself distinguishes the two callers unless
// the operation name does.
func TestGrepIgnoreDiscoveryIsChargedToTheBudget(t *testing.T) {
	const dirCount = 40
	const budget = 8
	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, budget)

	_, err := NewLocalExecutionEnvironment(root).grepNative(t.Context(), "needle", root, "", false, 100, "")
	if err == nil {
		t.Fatalf("grepNative over a %d-directory tree with a listing budget of %d returned no error; ignore discovery is not charged to the budget", dirCount, budget)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("budget refusal reported %v, which the walk skips silently; it must fail the grep visibly instead", err)
	}
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("grepNative error = %v (%T), want a *globBudgetError", err, err)
	}
	if budgetErr.op != "grep" {
		t.Fatalf("globBudgetError.op = %q, want %q (grepNative's ignore discovery, not glob's)", budgetErr.op, "grep")
	}
}

// TestGrepWalkIsChargedToTheBudget proves grepNative's own directory walk
// charges every directory it visits to the shared budget, on top of whatever
// ignore discovery already charged for the same tree, so a huge tree
// grepNative walks after ignore discovery completes still costs unbounded
// work.
func TestGrepWalkIsChargedToTheBudget(t *testing.T) {
	const dirCount = 30
	const budget = 40
	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, budget)

	_, err := NewLocalExecutionEnvironment(root).grepNative(t.Context(), "needle", root, "", false, 100, "")
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("grepNative's own walk over a %d-directory tree with a listing budget of %d = (_, %v), want a *globBudgetError; grepWalk charges nothing to the budget", dirCount, budget, err)
	}
	if budgetErr.kind != budgetListings {
		t.Fatalf("globBudgetError.kind = %v, want budgetListings", budgetErr.kind)
	}
	if budgetErr.op != "grep" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "grep")
	}
}

// TestGrepWalkDoesNotChargeSkippedDirectories proves grepNative's own walk
// charges the listing budget only for directories it actually descends into.
// The d.IsDir() block in grepNative's callback charges budget.listing(true)
// only after the dot-directory and gitignore filepath.SkipDir checks earlier
// in the same callback run, so a directory the walk immediately skips costs
// nothing. The tree here mixes both kinds of exclusion the callback applies —
// dot-prefixed directories and directories a root .gitignore matches — and
// its excluded directories alone outnumber the lowered listing budget, while
// only a handful of directories are ever actually listed. loadIgnoreSet runs
// first over the same tree on the same budget, and it skips only the
// dot-prefixed directories, not the gitignored ones, so its own pass already
// charges the base plus every real and gitignored directory (1 + 3 + 10 = 14
// listings) before grepNative's walk begins. grepWalk's own pass then adds 4
// more — the base plus the 3 real directories it actually descends into —
// for a total of 18, comfortably under the budget of 25 and comfortably
// above ignore discovery's 14-listing cost alone, so headroom cannot hide a
// regression here. Moving the charge above the skip checks would instead
// start charging the 20 dot and 10 gitignored directories the walk was about
// to skip anyway, which on their own are more than enough to cross the
// budget of 25 partway through (the walk gives up as soon as the running
// total does, well short of a legitimate 18): that is the only way this test
// can fail.
func TestGrepWalkDoesNotChargeSkippedDirectories(t *testing.T) {
	root := t.TempDir()

	const realCount = 3
	for i := range realCount {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("dir%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "dir00", "leaf.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const dotCount = 20
	for i := range dotCount {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf(".excluded%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const gitignoredCount = 10
	gitignoreLines := make([]string, 0, gitignoredCount)
	for i := range gitignoredCount {
		dir := fmt.Sprintf("ignored%02d", i)
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		gitignoreLines = append(gitignoreLines, dir+"/")
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(strings.Join(gitignoreLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const budget = 25
	stubMaxGlobDirListings(t, budget)

	out, err := NewLocalExecutionEnvironment(root).grepNative(t.Context(), "needle", root, "", false, 100, "")
	if budgetErr, refused := errors.AsType[*globBudgetError](err); refused {
		t.Fatalf("grepNative over a tree with %d dot-excluded and %d gitignored directories against only %d listed directories, with a listing budget of %d well above ignore discovery's own cost, refused: %v; the walk is charging directories it is about to skip toward the budget instead of only the ones it actually descends into", dotCount, gitignoredCount, realCount, budget, budgetErr)
	}
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if !strings.Contains(out, "needle") {
		t.Fatalf("grepNative(%q) = %q, want a match for the needle in dir00/leaf.txt", "needle", out)
	}
}

// TestGrepWalkStopsOnADirectoryWithTooManyEntries pins grep's ignore
// discovery surfacing the entries refusal, not grepNative's own walk. root
// itself is the one oversized directory here (30 files, no subdirectories),
// and loadIgnoreSet's fs.WalkDir walk lists it before grepNative's own walk
// ever starts, so the refusal always comes from ignore discovery's callback
// swallowing-guard. It cannot also pin the equivalent guard in grepNative's
// own walk callback: that guard only matters if a directory grows past the
// entries bound between ignore discovery's pass and the walk's own, and no
// static tree can produce that — ignore discovery's skip set is always a
// subset of the walk's, so ignore discovery always reaches (and refuses) any
// oversized directory first. Only concurrent growth reaches it, which
// TestGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery
// forces by growing the directory inside a stubbed walk.
func TestGrepWalkStopsOnADirectoryWithTooManyEntries(t *testing.T) {
	const fileCount = 30
	const budget = 10
	root := flatEntriesFixture(t, fileCount)
	stubMaxGlobDirEntries(t, budget)
	stubGlobDirChunk(t, 5)

	out, err := NewLocalExecutionEnvironment(root).grepNative(t.Context(), "needle", root, "", false, 100, "")
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("grepNative over a %d-entry directory with an entry budget of %d = (%q, %v), want a *globBudgetError; grep's walks are swallowing the refusal as if it were an unreadable entry", fileCount, budget, out, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "grep" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "grep")
	}
}

// TestGlobIgnoreDiscoveryPropagatesTheLiveEntryRefusal pins that discovery
// reports the live-entry ceiling rather than absorbing it into its
// best-effort arm. Its walk starts at the scope's prefix and holds the whole
// chain below it live, so reaching the ceiling there means the process is
// already carrying the memory the ceiling exists to prevent; continuing and
// leaving a later walk to refuse would spend it first, and would hand back an
// ignoreSet that stopped partway while reporting itself complete.
//
// This drives loadIgnoreSet directly. Through a glob call the two passes now
// have the same reach by design, so a recursive pattern's own walk refuses on
// the same tree whether or not discovery propagated, and the outcomes are
// indistinguishable. What is being pinned is which pass reports it.
func TestGlobIgnoreDiscoveryPropagatesTheLiveEntryRefusal(t *testing.T) {
	const perDir = 8
	const depth = 6
	const ceiling = 20

	root := t.TempDir()
	dir := root
	for i := range depth {
		dir = filepath.Join(dir, fmt.Sprintf("level%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := range perDir {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.txt", j)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	stubMaxGlobLiveEntries(t, ceiling)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	_, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over a %d-level tree holding %d entries per level against a ceiling of %d = %v, want a *globBudgetError; the refusal is being absorbed by discovery's best-effort arm", depth, perDir, ceiling, err)
	}
	if budgetErr.kind != budgetLiveEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetLiveEntries", budgetErr.kind)
	}
}

// TestGlobBudgetErrorAdviceDependsOnTheOperationAndTheBound pins that advice
// and entryAdvice each name the lever that can actually fix the refusal they
// describe, not one that only sounds plausible. A model acts on this advice
// directly: a grep's pattern is a regex applied to file contents after the
// walk has already listed everything, so narrowing it cannot reduce how much
// the walk lists, while a glob's pattern controls what gets listed in the
// first place, and one oversized directory is a different lever again from a
// whole call's listing count. Advice that names the wrong lever sends a model
// off to change something that cannot help, so this asserts the three
// distinctions structurally instead of embedding any method's wording:
// collapsing any one of them to a single return value still passes every
// other test in this package.
func TestGlobBudgetErrorAdviceDependsOnTheOperationAndTheBound(t *testing.T) {
	grepListings := &globBudgetError{op: "grep", kind: budgetListings}
	globListings := &globBudgetError{op: "glob", kind: budgetListings}
	if grepAdvice, globAdvice := grepListings.advice(), globListings.advice(); grepAdvice == globAdvice {
		t.Fatalf("advice() collapsed the grep/glob distinction for a listings refusal: grep = %q, glob = %q; narrowing a grep's pattern cannot reduce how much it lists, so the two operations need different advice", grepAdvice, globAdvice)
	}

	grepEntries := &globBudgetError{op: "grep", kind: budgetEntries}
	globEntries := &globBudgetError{op: "glob", kind: budgetEntries}
	if grepEntryAdvice, globEntryAdvice := grepEntries.entryAdvice(), globEntries.entryAdvice(); grepEntryAdvice == globEntryAdvice {
		t.Fatalf("entryAdvice() collapsed the grep/glob distinction for an entries refusal: grep = %q, glob = %q; a grep cannot spell a pattern that lists less of one directory, so the two operations need different advice", grepEntryAdvice, globEntryAdvice)
	}

	if entryAdvice, callAdvice := globEntries.entryAdvice(), globListings.advice(); entryAdvice == callAdvice {
		t.Fatalf("entryAdvice() collapsed into advice() for the same operation: entryAdvice = %q, advice = %q; one oversized directory and a whole call's listing count are not the same lever, so they need different advice", entryAdvice, callAdvice)
	}
}

// patternScopeFixture builds a t.TempDir() holding a small sub/ (one .txt
// file, matching a "sub/*.txt"-shaped pattern) beside an oversized, unrelated
// huge/ (hugeCount files, enough to trip a lowered per-directory entry cap on
// its own), so a test can prove ignore discovery scoped to sub/ never reads
// huge/'s entries.
func patternScopeFixture(t *testing.T, hugeCount int) (root string) {
	t.Helper()
	root = t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(root, "huge")
	if err := os.MkdirAll(huge, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range hugeCount {
		if err := os.WriteFile(filepath.Join(huge, fmt.Sprintf("leaf%03d.dat", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobIgnoreDiscoverySkipsDirectoriesThePatternCannotReach proves ignore
// discovery's scope matches a literal pattern prefix's reach. huge/'s file
// count trips a lowered per-directory entry cap, but "sub/*.txt"'s own
// pattern walk never visits huge/ at all, so ignore discovery must not visit
// it either: a sibling directory the pattern walk would never touch cannot be
// allowed to fail a glob that has nothing to do with it.
func TestGlobIgnoreDiscoverySkipsDirectoriesThePatternCannotReach(t *testing.T) {
	const hugeCount = 20
	root := patternScopeFixture(t, hugeCount)
	stubMaxGlobDirEntries(t, 10)

	var read int
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		return boundedDirFS{FS: pacedDirEntriesFS{FS: os.DirFS(dir), read: &read, pace: 1 << 30}, budget: budget, ctx: ctx}
	})

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "sub/*.txt", root, false)
	if err != nil {
		t.Fatalf(`Glob("sub/*.txt") over a base with a %d-file huge/ sibling and a lowered entry cap = (%v, %v), want sub's file and no error; ignore discovery is reading directories the pattern walk can never reach`, hugeCount, matches, err)
	}
	want := []string{filepath.Join(root, "sub", "keep.txt")}
	if !slices.Equal(matches, want) {
		t.Fatalf("Glob(%q) matches = %v, want %v", "sub/*.txt", matches, want)
	}
	if read >= hugeCount {
		t.Fatalf("ignore discovery (or the pattern walk) read %d entries, enough to have read all of huge/'s %d files; discovery's scope has widened back out to the whole base", read, hugeCount)
	}
}

// TestGlobIgnoreDiscoveryStillCoversEverythingForAWildcardPattern guards
// against TestGlobIgnoreDiscoverySkipsDirectoriesThePatternCannotReach's fix
// collapsing into "ignore discovery is never budgeted": doublestar.SplitPattern
// gives "." for a pattern starting with a metacharacter, so "**/*.txt"'s own
// pattern walk covers the whole base too, and huge/ tripping the entry cap
// must still refuse the call.
func TestGlobIgnoreDiscoveryStillCoversEverythingForAWildcardPattern(t *testing.T) {
	const hugeCount = 20
	root := patternScopeFixture(t, hugeCount)
	stubMaxGlobDirEntries(t, 10)

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.txt", root, false)
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf(`Glob("**/*.txt") over a base with a %d-file huge/ sibling and a lowered entry cap = (%v, %v), want a *globBudgetError`, hugeCount, matches, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
}

// TestGlobIgnoreDiscoveryAppliesAnAncestorGitignoreAboveThePrefix proves the
// ancestors-plus-prefix split in loadIgnoreSet loses no rules: a .gitignore
// at the base excluding a path under sub/ must still exclude it from a
// "sub/*.txt" glob, even though discovery no longer walks the base itself to
// find that .gitignore.
func TestGlobIgnoreDiscoveryAppliesAnAncestorGitignoreAboveThePrefix(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "skip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("sub/skip.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "sub/*.txt", root, false)
	if err != nil {
		t.Fatalf(`Glob("sub/*.txt") = (%v, %v), want sub/keep.txt and no error`, matches, err)
	}
	want := []string{filepath.Join(root, "sub", "keep.txt")}
	if !slices.Equal(matches, want) {
		t.Fatalf("Glob(%q) matches = %v, want %v (sub/skip.txt should be excluded by the base .gitignore)", "sub/*.txt", matches, want)
	}
}

// TestGlobIgnoreDiscoveryStillPrunesADotDirectoryNamedAsThePrefix pins that
// the subtree walk applies its dot-directory prune to the prefix directory
// itself, not only to directories below it. Discovery starts its walk AT the
// prefix, so a check written against the prefix rather than against the
// walk's own root would exempt exactly the one directory the caller named,
// and discovery would descend into a dot-directory the walk is supposed to
// prune. Here that would cost the entry cap on ".config/huge", which the
// pattern's own listing of ".config" never touches.
func TestGlobIgnoreDiscoveryStillPrunesADotDirectoryNamedAsThePrefix(t *testing.T) {
	const hugeFiles = 20
	const entryCap = 10

	root := t.TempDir()
	dotDir := filepath.Join(root, ".config")
	huge := filepath.Join(dotDir, "huge")
	if err := os.MkdirAll(huge, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range hugeFiles {
		if err := os.WriteFile(filepath.Join(huge, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobDirEntries(t, entryCap)

	_, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), ".config/*.txt", root, false)
	if budgetErr, refused := errors.AsType[*globBudgetError](err); refused {
		t.Fatalf("Glob(\".config/*.txt\") = %v, want no refusal: ignore discovery descended into the dot-directory it names as its prefix and paid for %s, which the pattern's own listing never reads", budgetErr, "huge")
	}
	if err != nil {
		t.Fatalf("Glob(\".config/*.txt\") = %v, want no error", err)
	}
}

// TestLoadIgnoreSetConsultsSkipForTheScopePrefixItself pins the masking half
// of the same rule the dot-directory test above pins: the subtree walk's skip
// check is written against the walk's own root, so it covers the prefix
// directory as well as everything below it. Discovery starts its walk AT the
// prefix, and a check written against the prefix instead would exempt exactly
// the directory the caller named — letting a pattern whose literal prefix is
// a masked directory have discovery list it and read the rules inside it.
// secureDirFS enforces symlink refusal and root confinement but not masking;
// this skip is the only thing that supplies it.
//
// This drives loadIgnoreSet directly because the effect is not visible in a
// glob's results: the bypass reads a masked directory's .gitignore, whose
// rules only ever apply to paths under that same masked directory, and those
// are dropped from the answer anyway. What is wrong is the reading, so that
// is what this observes.
func TestLoadIgnoreSetConsultsSkipForTheScopePrefixItself(t *testing.T) {
	fsys := fstest.MapFS{
		"vault/.gitignore": &fstest.MapFile{Data: []byte("*.log\n")},
		"vault/keep.txt":   &fstest.MapFile{Data: []byte("x")},
	}

	var asked []string
	skip := func(relPath string) bool {
		asked = append(asked, relPath)
		return relPath == "vault"
	}

	set, err := loadIgnoreSet(t.Context(), fsys, skip, newGlobBudget("glob"), []ignoreScope{{prefix: "vault", depth: -1, walk: true}})
	if err != nil {
		t.Fatalf("loadIgnoreSet scoped to a masked prefix: %v", err)
	}
	if !slices.Contains(asked, "vault") {
		t.Fatalf("skip was never consulted about the prefix itself; it was asked about %v, so a masked directory named as a pattern's literal prefix would be walked and read", asked)
	}
	for _, d := range set.dirs {
		if d.rel == "vault" || strings.HasPrefix(d.rel, "vault/") {
			t.Fatalf("collected a rule from %q inside the masked prefix; masking is the only thing keeping discovery out of that subtree", d.rel)
		}
	}
}

// TestLoadIgnoreSetSkipsAMaskedAncestorGitignoreFile pins the file-level half
// of the masking check on the ancestor read. Masking is per path: a directory
// that is not masked can still hold a masked .gitignore, and the base itself
// is never masked while a .gitignore directly inside it can be. secureDirFS
// enforces symlink refusal and root confinement but not masking, so if the
// ancestor read checks only the directory it reads a file the policy hides —
// the same class as naming a masked directory as a pattern's prefix, one
// level finer.
//
// Like that test this drives loadIgnoreSet directly, because the effect is in
// what gets read rather than in the answer: rules from a masked ancestor
// would apply to paths the caller can see, so reading them is both a leak and
// a wrong exclusion.
func TestLoadIgnoreSetSkipsAMaskedAncestorGitignoreFile(t *testing.T) {
	fsys := fstest.MapFS{
		"sub/.gitignore":      &fstest.MapFile{Data: []byte("*.log\n")},
		"sub/deep/keep.txt":   &fstest.MapFile{Data: []byte("x")},
		"sub/deep/.gitignore": &fstest.MapFile{Data: []byte("*.tmp\n")},
	}

	var asked []string
	skip := func(relPath string) bool {
		asked = append(asked, relPath)
		// The directory is visible; only the rules file inside it is masked.
		return relPath == "sub/.gitignore"
	}

	set, err := loadIgnoreSet(t.Context(), fsys, skip, newGlobBudget("glob"), []ignoreScope{{prefix: "sub/deep", depth: -1, walk: true}})
	if err != nil {
		t.Fatalf("loadIgnoreSet with a masked ancestor .gitignore: %v", err)
	}
	if !slices.Contains(asked, "sub/.gitignore") {
		t.Fatalf("skip was never consulted about the ancestor rules file itself; it was asked about %v, so a masked .gitignore inside an unmasked directory would be read", asked)
	}
	for _, d := range set.dirs {
		if d.rel == "sub" {
			t.Fatalf("collected rules from the masked ancestor .gitignore at %q; masking is the only thing keeping discovery out of that file", d.rel)
		}
	}
}

// nonRecursiveScopeFixture builds a base holding one small file the caller's
// pattern can match and one large unrelated subtree beneath it, sized so that
// walking into the subtree trips the per-directory entry cap while listing
// the base alone does not.
func nonRecursiveScopeFixture(t *testing.T, hugeFiles int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(root, "huge")
	if err := os.MkdirAll(huge, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range hugeFiles {
		if err := os.WriteFile(filepath.Join(huge, fmt.Sprintf("f%02d.go", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobIgnoreDiscoveryStopsAtANonRecursivePatternsDepth pins that a pattern
// with no ** confines discovery to its own reach. Only ** matches across a
// separator, so "*.go" can only ever match files directly in the base: the
// rules that can touch one of its candidates live in the base and nowhere
// below it. Walking the whole subtree anyway inspects directories the
// pattern's own walk never lists, and one oversized directory down there then
// fails a glob that could never have looked inside it.
func TestGlobIgnoreDiscoveryStopsAtANonRecursivePatternsDepth(t *testing.T) {
	const hugeFiles = 20
	const entryCap = 10
	root := nonRecursiveScopeFixture(t, hugeFiles)
	stubMaxGlobDirEntries(t, entryCap)

	matches, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "*.go", root, false)
	if err != nil {
		t.Fatalf("Glob(\"*.go\") = %v, want top.go and no error: discovery walked into a subtree the pattern can never match inside", err)
	}
	if len(matches) != 1 || !strings.HasSuffix(matches[0], "top.go") {
		t.Fatalf("Glob(\"*.go\") = %v, want just top.go", matches)
	}
}

// TestGlobIgnoreDiscoveryStillWalksTheSubtreeForARecursivePattern is the
// other half of scoping by depth: ** reaches every descendant, so discovery
// has to descend too or it never loads the nested .gitignore files whose
// rules cover what the pattern matches down there. Without this, scoping
// could quietly collapse into "discovery never descends", which would
// silently under-exclude every nested rule.
//
// It asserts on exclusion rather than on a refusal: a budget refusal for a
// recursive pattern comes from the pattern's own walk over the same subtree,
// so it holds whatever discovery does and would pin nothing.
func TestGlobIgnoreDiscoveryStillWalksTheSubtreeForARecursivePattern(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("skipme.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skipme.go", "keep.go"} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "**/*.go", root, false)
	if err != nil {
		t.Fatalf("Glob(\"**/*.go\"): %v", err)
	}
	for _, m := range matches {
		if strings.HasSuffix(m, "skipme.go") {
			t.Fatalf("Glob(\"**/*.go\") returned %q, which sub/.gitignore excludes; discovery never descended to read that nested rules file", m)
		}
	}
	if excluded == 0 {
		t.Fatalf("Glob(\"**/*.go\") = (%v, excluded=0), want the nested .gitignore to have excluded skipme.go", matches)
	}
}

// TestLoadIgnoreSetStartsEachScopeWalkWithACleanLiveSet pins that every
// prefix in a multi-prefix scope is its own traversal. holdEntries releases
// what is not an ancestor of the directory being listed, which already
// separates sibling prefixes, but it cannot separate nested ones: a scope
// rooted at the base finishes still holding the base's listing, and the base
// is an ancestor of everything, so the next scope's walk inherits it and
// refuses on memory nothing occupies any more.
//
// The base and the subdirectory each fit the ceiling on their own and do not
// together, so this call succeeds only if the first walk's holdings are
// released before the second begins.
func TestLoadIgnoreSetStartsEachScopeWalkWithACleanLiveSet(t *testing.T) {
	const rootFiles = 12
	const subFiles = 12
	const ceiling = 20

	root := t.TempDir()
	for i := range rootFiles {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("r%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range subFiles {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("s%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobLiveEntries(t, ceiling)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	scope := []ignoreScope{{prefix: ".", depth: 0, walk: true}, {prefix: "sub", depth: 0, walk: true}}
	if _, err := loadIgnoreSet(t.Context(), fsys, nil, budget, scope); err != nil {
		t.Fatalf("loadIgnoreSet over a base of %d entries and a nested prefix of %d against a ceiling of %d = %v, want no refusal: the base walk's listing is no longer held once its traversal ends", rootFiles+1, subFiles, ceiling, err)
	}
}

// TestLoadIgnoreSetRefusesAnOversizedRulesFile pins the per-file byte bound.
// Directory entries are capped, but a .gitignore is bytes rather than
// records, so one enormous rules file walks straight past every entry bound
// and is read whole into memory.
//
// The assertion is on how much was actually read: refusing only after
// materializing the file would spend exactly the memory the bound exists to
// prevent, so the count must stop at the cap rather than reach the file's
// real size.
func TestLoadIgnoreSetRefusesAnOversizedRulesFile(t *testing.T) {
	const fileCap = 512
	const fileBytes = 64 * 1024

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), bytes.Repeat([]byte("a\n"), fileBytes/2), 0o644); err != nil {
		t.Fatal(err)
	}
	stubMaxGlobIgnoreFileBytes(t, fileCap)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	_, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over a %d-byte .gitignore with a per-file cap of %d = %v, want a *globBudgetError", fileBytes, fileCap, err)
	}
	if budgetErr.kind != budgetRulesFile {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesFile", budgetErr.kind)
	}
	if budgetErr.count > fileCap+1 {
		t.Fatalf("read %d bytes before refusing, want no more than the cap of %d plus the byte that proves it was exceeded: the file was materialized first", budgetErr.count, fileCap)
	}
}

// TestLoadIgnoreSetRefusesTooManyRetainedRules pins the aggregate bound the
// per-file one cannot: every compiled matcher is held for the whole call, so
// a tree carrying a rules file in each of very many directories retains
// unbounded memory while no single file is anywhere near the per-file cap.
//
// The assertion is on what was retained rather than on the returned set,
// since a refusal that arrived only after every file had been compiled would
// already be holding what the bound is meant to keep out.
func TestLoadIgnoreSetRefusesTooManyRetainedRules(t *testing.T) {
	const dirs = 40
	const perFile = 64
	const total = 512

	root := t.TempDir()
	for i := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), bytes.Repeat([]byte("b\n"), perFile/2), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobIgnoreTotalBytes(t, total)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over %d directories each carrying a %d-byte .gitignore, against a retention budget of %d = %v, want a *globBudgetError", dirs, perFile, total, err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if budget.ruleBytes > total+perFile {
		t.Fatalf("retained %d bytes before refusing, want no more than the budget of %d plus the one file that crossed it: every rules file was compiled before anything refused", budget.ruleBytes, total)
	}
	if len(set.dirs) >= dirs {
		t.Fatalf("retained matchers for all %d directories, want fewer: the walk kept compiling past the budget", dirs)
	}
}

// stubMaxGlobIgnoreFileBytes lowers the per-file .gitignore byte cap for a
// test and restores it when the test ends.
func stubMaxGlobIgnoreFileBytes(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobIgnoreFileBytes
	maxGlobIgnoreFileBytes = n
	t.Cleanup(func() { maxGlobIgnoreFileBytes = orig })
}

// stubMaxGlobIgnoreTotalBytes lowers the call-wide .gitignore retention
// budget for a test and restores it when the test ends.
func stubMaxGlobIgnoreTotalBytes(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobIgnoreTotalBytes
	maxGlobIgnoreTotalBytes = n
	t.Cleanup(func() { maxGlobIgnoreTotalBytes = orig })
}

// nestedHugeScopeFixture builds a base holding one matchable file, a small
// subdirectory, and an oversized directory beneath that subdirectory. Reading
// the base or the subdirectory alone stays under the per-directory entry cap;
// descending into the oversized one does not.
func nestedHugeScopeFixture(t *testing.T, hugeFiles int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	huge := filepath.Join(sub, "huge")
	if err := os.MkdirAll(huge, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range hugeFiles {
		if err := os.WriteFile(filepath.Join(huge, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobIgnoreDiscoveryTreatsOnlyAStandaloneDoubleStarAsRecursive pins that
// the scope's recursion test matches doublestar's own rule rather than
// looking for the two characters anywhere in the pattern. Only a ** that is a
// whole path component crosses a separator; a ** embedded in a longer
// component behaves like a single star, and an escaped one or one inside a
// character class is not a wildcard at all. Treating any of those as
// recursive sends discovery through a subtree the glob can never reach, and
// one oversized directory down there then fails a call that could not have
// looked inside it.
//
// Each shape is asserted against the same fixture, so a failure names the
// pattern whose scope widened.
func TestGlobIgnoreDiscoveryTreatsOnlyAStandaloneDoubleStarAsRecursive(t *testing.T) {
	const hugeFiles = 20
	const entryCap = 10

	for _, pattern := range []string{
		`foo**bar.txt`, // ** inside a longer component: one star's reach
		`sub/\**.txt`,  // an escaped star beside a wildcard, not a **
		`sub/[**].txt`, // a character class that happens to contain stars
	} {
		t.Run(pattern, func(t *testing.T) {
			root := nestedHugeScopeFixture(t, hugeFiles)
			stubMaxGlobDirEntries(t, entryCap)

			_, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), pattern, root, false)
			if budgetErr, refused := errors.AsType[*globBudgetError](err); refused {
				t.Fatalf("Glob(%q) = %v, want no refusal: %q holds no standalone ** component, so discovery must not walk the subtree beneath it", pattern, budgetErr, pattern)
			}
			if err != nil {
				t.Fatalf("Glob(%q) = %v, want no error", pattern, err)
			}
		})
	}
}

// TestNarrowIgnoreScopesSubsumesANestedScopeWithinReach pins that a bounded
// ancestor scope absorbs a nested one it already covers. Discovery walks each
// surviving scope in turn against one shared budget, so keeping a nested
// scope an ancestor's own reach already includes lists those directories
// twice and compiles their rules twice, for nothing.
func TestNarrowIgnoreScopesSubsumesANestedScopeWithinReach(t *testing.T) {
	// "." reaching two levels already covers "a" reaching one.
	got := narrowIgnoreScopes([]ignoreScope{{prefix: ".", depth: 2, walk: true}, {prefix: "a", depth: 1, walk: true}})
	if len(got) != 1 || got[0].prefix != "." || got[0].depth != 2 {
		t.Fatalf("narrowIgnoreScopes = %v, want just the base scope: its reach of 2 already covers \"a\" at depth 1", got)
	}

	// One level short: "a" reaches a level the base scope does not, so both stay.
	got = narrowIgnoreScopes([]ignoreScope{{prefix: ".", depth: 1, walk: true}, {prefix: "a", depth: 1, walk: true}})
	if len(got) != 2 {
		t.Fatalf("narrowIgnoreScopes = %v, want both scopes: the base reaches 1 level and \"a\" needs 2", got)
	}
}

// TestGlobIgnoreDiscoveryDoesNotListALiteralDirectoryPattern pins that a
// pattern with no glob metacharacter costs discovery no listing. Such a
// pattern names one exact path, which the glob resolves by stat'ing it — it
// never lists the directory holding it. Discovery listing that directory
// anyway is work the pattern's own walk would not do, so a large literal
// directory fails a call that could not have listed it.
//
// The rules that reach a literal candidate live in its ancestors, and the
// second assertion keeps that half honest: the ancestor .gitignore must still
// exclude, so this cannot be satisfied by loading no rules at all.
func TestGlobIgnoreDiscoveryDoesNotListALiteralDirectoryPattern(t *testing.T) {
	const hugeFiles = 20
	const entryCap = 10

	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A rules file beside the candidate, and enough entries that listing this
	// directory would trip the cap.
	if err := os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("hidden.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "wanted.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "hidden.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range hugeFiles {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("bulk%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobDirEntries(t, entryCap)

	env := NewLocalExecutionEnvironment(root)
	matches, _, err := env.GlobWithExclusions(t.Context(), "sub/wanted.txt", root, false)
	if err != nil {
		t.Fatalf("Glob(\"sub/wanted.txt\") = %v, want the file and no error: the pattern names one path, so discovery must not list the directory holding it", err)
	}
	if len(matches) != 1 || !strings.HasSuffix(matches[0], "wanted.txt") {
		t.Fatalf("Glob(\"sub/wanted.txt\") = %v, want just wanted.txt", matches)
	}

	// The ancestor rules file is still read, without listing: the candidate it
	// excludes must not come back.
	hidden, _, err := env.GlobWithExclusions(t.Context(), "sub/hidden.txt", root, false)
	if err != nil {
		t.Fatalf("Glob(\"sub/hidden.txt\") = %v, want no error", err)
	}
	if len(hidden) != 0 {
		t.Fatalf("Glob(\"sub/hidden.txt\") = %v, want nothing: sub/.gitignore excludes it, so those rules were never loaded", hidden)
	}
}

// TestLoadIgnoreSetReadsEachRulesFileOnce pins that partially overlapping
// scopes do not read the same .gitignore twice. narrowIgnoreScopes drops a
// scope another fully covers, but two scopes can overlap without either
// covering the other, and a duplicate read compiles a second matcher, retains
// it, and charges its bytes again — enough to trip the rules budget on a brace
// pattern that is otherwise unremarkable.
//
// The base reaching one level and a nested prefix reaching one more overlap on
// "a" exactly, so a duplicate shows up as a doubled rule count.
func TestLoadIgnoreSetReadsEachRulesFileOnce(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Join(root, "a"), deep} {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("x.log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	scope := []ignoreScope{
		{prefix: ".", depth: 1, walk: true},
		{prefix: "a", depth: 1, walk: true},
	}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, scope)
	if err != nil {
		t.Fatalf("loadIgnoreSet over two overlapping scopes: %v", err)
	}

	seen := map[string]int{}
	for _, d := range set.dirs {
		seen[d.rel]++
	}
	for rel, n := range seen {
		if n != 1 {
			t.Fatalf("rules from %q retained %d times, want once: overlapping scopes read the same .gitignore twice", rel, n)
		}
	}
}

// TestRetainRuleBytesChargesPerFileOverhead pins that an empty .gitignore is
// not free. Charging source bytes alone lets a tree carrying an empty rules
// file in every directory retain an entry and a compiled matcher apiece while
// the byte budget stays untouched, so the budget stops bounding how many
// files are retained at all.
func TestRetainRuleBytesChargesPerFileOverhead(t *testing.T) {
	const total = 4 * 512
	stubMaxGlobIgnoreTotalBytes(t, total)

	budget := newGlobBudget("glob")
	var accepted int
	for range 100 {
		if err := budget.retainRuleBytes(0); err != nil {
			break
		}
		accepted++
	}
	if accepted >= 100 {
		t.Fatalf("retained %d empty rules files against a budget of %d bytes with no refusal; an empty file must still cost its entry and matcher", accepted, total)
	}
	if budget.ruleBytes == 0 {
		t.Fatalf("ruleBytes = 0 after retaining %d empty files, want the per-file overhead charged", accepted)
	}
}

// TestLoadIgnoreSetSkipsADotDirectoryAncestor pins that the ancestor read
// applies the dot-directory exclusion the subtree walk applies. isDotPath
// drops every candidate underneath a dot-directory before a rule from one
// could apply, so reading such a rules file cannot change an answer — it only
// spends the rules budget, and it is the one asymmetry between the two halves
// of discovery that had no reason behind it.
//
// This is a separate concern from masking: masking is per path and set by
// policy, while this is the same dotfile convention the match filter uses.
func TestLoadIgnoreSetSkipsADotDirectoryAncestor(t *testing.T) {
	fsys := fstest.MapFS{
		".config/.gitignore":   &fstest.MapFile{Data: []byte("*.log\n")},
		".config/sub/keep.txt": &fstest.MapFile{Data: []byte("x")},
		".gitignore":           &fstest.MapFile{Data: []byte("root.log\n")},
	}

	set, err := loadIgnoreSet(t.Context(), fsys, nil, newGlobBudget("glob"), []ignoreScope{{prefix: ".config/sub", depth: 0, walk: true}})
	if err != nil {
		t.Fatalf("loadIgnoreSet scoped under a dot-directory: %v", err)
	}
	for _, d := range set.dirs {
		if d.rel == ".config" {
			t.Fatalf("retained rules from the dot-directory ancestor %q; every candidate below it is already dropped, so reading it only spends budget", d.rel)
		}
	}
}

// TestGlobAppliesRulesInsideAnEscapedMetacharacterDirectory pins the exclusion
// path for a pattern whose literal prefix contains an escaped metacharacter,
// so the directory it names is really called "*". The prefix has to reach that
// directory, or discovery reads rules only through its parent, misses the
// rules file inside it, and returns a path the caller asked to have excluded —
// a wrong answer rather than a refusal.
func TestGlobAppliesRulesInsideAnEscapedMetacharacterDirectory(t *testing.T) {
	root := t.TempDir()
	starred := filepath.Join(root, "a", "*")
	if err := os.MkdirAll(starred, 0o755); err != nil {
		t.Skipf("cannot create a directory named \"*\" on this platform: %v", err)
	}
	if err := os.WriteFile(filepath.Join(starred, ".gitignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secret.txt", "target.txt"} {
		if err := os.WriteFile(filepath.Join(starred, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := NewLocalExecutionEnvironment(root)

	excluded, _, err := env.GlobWithExclusions(t.Context(), `a/\*/secret.txt`, root, false)
	if err != nil {
		t.Fatalf(`Glob("a/\*/secret.txt") = %v, want no error`, err)
	}
	if len(excluded) != 0 {
		t.Fatalf(`Glob("a/\*/secret.txt") = %v, want nothing: the .gitignore inside the directory named "*" excludes it, so those rules were never loaded`, excluded)
	}

	// The other half: the same shape must still match what is not excluded, so
	// this cannot be satisfied by matching nothing at all.
	kept, _, err := env.GlobWithExclusions(t.Context(), `a/\*/target.txt`, root, false)
	if err != nil {
		t.Fatalf(`Glob("a/\*/target.txt") = %v, want no error`, err)
	}
	if len(kept) != 1 {
		t.Fatalf(`Glob("a/\*/target.txt") = %v, want the one file`, kept)
	}
}

// TestTruncatedAtReportsTheCapThatTripped pins that the reported cap is the
// one in force when truncation happened, not whatever the global holds when
// the caller gets around to reading it. Reading the global would misreport any
// time the two differ, and the value goes into a message a model acts on, so
// it has to describe the bound that actually cut the listing short.
func TestTruncatedAtReportsTheCapThatTripped(t *testing.T) {
	const trippedAt = 3

	budget := newGlobBudget("glob")
	restore := SetMaxGlobMatchesForTesting(trippedAt)
	for range trippedAt {
		if !budget.match() {
			t.Fatalf("match() refused before the cap of %d was reached", trippedAt)
		}
	}
	if budget.match() {
		t.Fatalf("match() allowed a %dth match against a cap of %d", trippedAt+1, trippedAt)
	}
	// The cap moves after the walk, as another test lowering it would do.
	restore()

	if got := budget.TruncatedAt(); got != trippedAt {
		t.Fatalf("TruncatedAt() = %d, want %d: it reported the current cap rather than the one that cut the listing short", got, trippedAt)
	}
}
