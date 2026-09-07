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
type pacedDirEntriesFS struct {
	fs.FS
	read *int
	pace int
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
	return &pacedDirEntriesFile{ReadDirFile: rdf, read: p.read, pace: p.pace}, nil
}

type pacedDirEntriesFile struct {
	fs.ReadDirFile
	read *int
	pace int
}

func (p *pacedDirEntriesFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if n > 0 && n > p.pace {
		n = p.pace
	}
	entries, err := p.ReadDirFile.ReadDir(n)
	*p.read += len(entries)
	return entries, err
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
	stubGlobBaseFS(t, func(dir string, budget *globBudget) fs.FS {
		counter = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget}}
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
	stubGlobBaseFS(t, func(dir string, budget *globBudget) fs.FS {
		full = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget}}
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
	stubGlobBaseFS(t, func(dir string, budget *globBudget) fs.FS {
		capped = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget}}
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
	root := t.TempDir()
	for i := range fileCount {
		p := filepath.Join(root, fmt.Sprintf("leaf%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const budget = 10
	stubMaxGlobDirEntries(t, budget)

	var read int
	var seenBudget *globBudget
	stubGlobBaseFS(t, func(dir string, callBudget *globBudget) fs.FS {
		seenBudget = callBudget
		return boundedDirFS{FS: pacedDirEntriesFS{FS: os.DirFS(dir), read: &read, pace: 5}, budget: callBudget}
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

// TestGrepWalkIsChargedToTheBudget proves grepNative's own directory walk has
// no bound of its own, distinct from the ignore-discovery bound
// TestGrepIgnoreDiscoveryIsChargedToTheBudget already covers: grepWalk is a
// plain fs.WalkDir with nothing charging its listings to a budget, so a huge
// tree grepNative walks after ignore discovery completes still costs
// unbounded work. The tree here has no .gitignore and is small enough that
// ignore discovery alone stays well under the lowered listing budget, so a
// refusal can only be attributed to the walk grepNative runs over the tree it
// is grepping, not to the ignore-discovery pass that already has its own
// test above.
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

// TestGlobIgnoreDiscoveryIsChargedToTheBudget proves loadIgnoreSet's own
// fs.WalkDir has no bound of its own: it runs to completion before the first
// globMatches call even exists, so a glob whose pattern walk touches almost
// nothing can still spend unbounded work collecting .gitignore rules across a
// huge tree. The pattern here names a file that does not exist anywhere in
// the tree and has no glob metacharacters, so doublestar's no-meta-character
// fast path resolves it with a single Stat and never lists a directory — any
// charge against the budget has to come from ignore discovery, not from the
// pattern walk. include_ignored skips ignore discovery entirely, so the
// identical call must not trip the same budget: that is the control that
// proves the failure is ignore discovery's, not some other unbounded walk.
// The error must name "glob" as the operation that spent the budget — the
// counterpart grep test proves the same budget names "grep" instead, which is
// how a caller told the two apart at all — and the countingFS proves the walk
// actually stopped at the budget rather than finishing and reporting a
// failure afterward.
func TestGlobIgnoreDiscoveryIsChargedToTheBudget(t *testing.T) {
	const dirCount = 40
	const budget = 8
	root := globBudgetFixture(t, dirCount)
	stubMaxGlobDirListings(t, budget)

	var counter *countingFS
	stubGlobBaseFS(t, func(dir string, budget *globBudget) fs.FS {
		counter = &countingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget}}
		return counter
	})

	matches, _, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "does-not-exist.txt", root, false)
	if err == nil {
		t.Fatalf("GlobWithExclusions(include_ignored=false) over a %d-directory tree with a listing budget of %d returned no error and %d matches; ignore discovery is not charged to the budget", dirCount, budget, len(matches))
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
		t.Fatalf("ignore discovery made %d directory listings against a budget of %d, want at most %d", counter.calls, budget, budget+1)
	}
	if counter.calls >= dirCount+1 {
		t.Fatalf("ignore discovery listed all %d directories instead of stopping early (%d listings made)", dirCount+1, counter.calls)
	}

	if _, _, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "does-not-exist.txt", root, true); err != nil {
		t.Fatalf("GlobWithExclusions(include_ignored=true) = %v, want nil (ignore discovery is skipped entirely, so it cannot trip the budget)", err)
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
// secureDirFS.ReadDir returns df.ReadDir(-1) unsorted, unlike os.DirFS (which
// sorts), so which files survive a cap tripping mid-listing is otherwise the
// filesystem's business, not the glob's. Every file is written in
// reverse-lexical order and given an identical mtime, so neither creation
// order nor modification time can accidentally line up with the alphabetical
// order the fix is supposed to guarantee, and two back-to-back runs over the
// unchanged tree must agree with each other and with that order.
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

	first, _, truncatedAt, err := env.GlobWithExclusions(t.Context(), "*.txt", worktree, true)
	if err != nil {
		t.Fatalf("first sandboxed glob: %v", err)
	}
	if truncatedAt != 4 {
		t.Fatalf("first run truncatedAt = %d, want 4", truncatedAt)
	}
	second, _, _, err := env.GlobWithExclusions(t.Context(), "*.txt", worktree, true)
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

// TestGlobIgnoreDiscoveryStopsOnADirectoryWithTooManyEntries covers the arm
// the pattern-walk entry test cannot reach: loadIgnoreSet walks the base with
// fs.WalkDir, whose callback treats every error it is handed as an unreadable
// entry to skip. A per-directory entry refusal handed to that callback is not
// an unreadable entry — it is the bound doing its job — so swallowing it both
// lets the oversized directory be read again by whatever walks next and
// leaves ignore discovery reporting a set it never finished collecting, which
// silently under-excludes the rest of the tree.
//
// The pattern is metacharacter-free and matches nothing, so doublestar
// resolves it with a stat and lists no directory at all: the only walk that
// can reach the entry bound here is ignore discovery's.
func TestGlobIgnoreDiscoveryStopsOnADirectoryWithTooManyEntries(t *testing.T) {
	const fileCount = 30
	const budget = 10
	root := flatEntriesFixture(t, fileCount)
	stubMaxGlobDirEntries(t, budget)
	stubGlobDirChunk(t, 5)

	matches, _, _, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(t.Context(), "does-not-exist.txt", root, false)
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("GlobWithExclusions(include_ignored=false) over a %d-entry directory with an entry budget of %d = (%v, %v), want a *globBudgetError; ignore discovery's walk is swallowing the refusal as if it were an unreadable entry", fileCount, budget, matches, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "glob" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "glob")
	}
}

// TestGrepWalkStopsOnADirectoryWithTooManyEntries is the same contract for
// grep's two best-effort walks. Both loadIgnoreSet's callback and grepNative's
// own walk callback skip past any error they are handed, so an oversized
// directory would be read in full by each of them in turn and the call would
// come back reporting no matches and no error at all.
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
