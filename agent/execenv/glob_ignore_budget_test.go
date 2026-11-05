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

	gitignore "github.com/sabhiram/go-gitignore"
)

// stubMaxGlobDirListings lowers the directory-listing budget for a test and
// restores it when the test ends, so a budget test can use a tree small
// enough for t.TempDir() rather than one large enough to trip the real bound.
// loadWholeIgnoreSet loads root's ignore set under a fresh glob budget and the
// whole base ignore scope, returning the set and the budget it was loaded
// under — the standard discovery shape of these tests.
func loadWholeIgnoreSet(t *testing.T, root string) (*ignoreSet, *GlobBudget, error) {
	t.Helper()
	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	return set, budget, err
}

// writeRootGitignore writes content to root's .gitignore.
func writeRootGitignore(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIgnoreScopeTreatsASingleTermBraceStarStarAsRecursive pins the Medium
// from the cdd6d21 review: {**} and {foo/**} reach Expand unchanged but match
// recursively in doublestar, so ignore discovery computed a bounded scope and
// skipped nested .gitignore files while the walk honored them. Expand now
// unwraps single-term braces, so the scope depth check sees the same **
// doublestar walks and the scope is unbounded.
func TestIgnoreScopeTreatsASingleTermBraceStarStarAsRecursive(t *testing.T) {
	for _, pattern := range []string{"{**}/*.txt", "{foo/**}/*.txt", "prefix/{**}/sfx/*.txt"} {
		expanded, err := expandSearchPattern(pattern)
		if err != nil {
			t.Fatalf("expandSearchPattern(%q): %v", pattern, err)
		}
		for _, scope := range ignoreScopeForPatterns(expanded) {
			if scope.depth != -1 {
				t.Fatalf("ignoreScopeForPatterns(%q) has bounded depth %d for scope %+v, want unbounded: the ** doublestar walks must widen discovery", pattern, scope.depth, scope)
			}
		}
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

	set, _, err := loadWholeIgnoreSet(t, root)
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
	writeRootGitignore(t, root, strings.Join(gitignoreLines, "\n")+"\n")

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
	writeRootGitignore(t, root, "sub/skip.txt\n")

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

// TestLoadIgnoreSetRefusesTooManyRetainedRules is the reproduction of #1018.
// The aggregate budget used to charge each .gitignore's SOURCE bytes, but
// go-gitignore compiles one regexp per rule line and a compiled regexp costs
// far more than the line that produced it, so a call could read a few hundred
// bytes of source and still retain far more matcher memory than the budget
// allowed. Every file here is tiny — its whole source is a small fraction of
// one compiled rule — so the fixture's combined source stays far under the
// budget while the matchers it compiles to blow straight past it. A budget
// that still charged source bytes lets this call through entirely.
//
// The assertion is on what was retained rather than on the returned set, since
// a refusal that arrived only after every file had been compiled would already
// be holding what the bound is meant to keep out.
func TestLoadIgnoreSetRefusesTooManyRetainedRules(t *testing.T) {
	const dirs = 40
	const rulesPerFile = 8
	// Room for two of these files once each pays for its rules and its source,
	// and not a third.
	const total = 24 * globIgnoreRuleBytes

	root := t.TempDir()
	var sourceBytes int
	for i := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rules := make([]string, rulesPerFile)
		for r := range rules {
			rules[r] = fmt.Sprintf("r%d.log", r)
		}
		data := []byte(strings.Join(rules, "\n") + "\n")
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		sourceBytes += len(data)
	}
	if sourceBytes >= total {
		t.Fatalf("fixture source totals %d bytes, want far under the %d-byte budget: this test has to fail only because the budget charges compiled rules rather than source bytes", sourceBytes, total)
	}
	stubMaxGlobIgnoreTotalBytes(t, total)

	set, budget, err := loadWholeIgnoreSet(t, root)
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over %d directories each carrying %d rules in a few dozen source bytes (combined source %d, far under the %d-byte budget) = %v, want a *globBudgetError: charging source bytes missed the %d compiled matchers entirely", dirs, rulesPerFile, sourceBytes, total, err, dirs*rulesPerFile)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if want := total + rulesPerFile*globIgnoreRuleBytes + sourceBytes/dirs; budget.ruleBytes > want {
		t.Fatalf("retained %d bytes before refusing, want no more than %d (the budget plus the one file that crossed it, rules and source together): every rules file was compiled before anything refused", budget.ruleBytes, want)
	}
	if len(set.dirs) != 2 {
		t.Fatalf("retained matchers for %d of %d directories, want exactly 2: the budget fits two files' rules and source and refuses the third", len(set.dirs), dirs)
	}
}

// TestLoadIgnoreSetChargesALongRulesSource is the second half of #1018's fix.
// A rule's matcher grows with the expression it was parsed from, not only with
// the count of rules, so a single line near the per-file cap compiles to a
// regexp far larger than the flat per-rule charge records. This one file holds
// about 256 KiB of source in a single rule — nowhere near the per-file cap —
// yet its matcher is far larger than a budget of a few dozen KiB. Charging only
// globIgnoreRuleBytes would let it through with the budget barely touched,
// which is the same undercount this issue exists to remove, just moved from
// short rules to long ones.
func TestLoadIgnoreSetChargesALongRulesSource(t *testing.T) {
	const lineBytes = 256 * 1024
	const total = 64 * 1024

	root := t.TempDir()
	long := strings.Repeat("a", lineBytes)
	writeRootGitignore(t, root, long+"\n")
	stubMaxGlobIgnoreTotalBytes(t, total)

	budget := newGlobBudget("glob")
	fsys := boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}
	_, err := loadIgnoreSet(t.Context(), fsys, nil, budget, wholeBaseIgnoreScope())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over one %d-byte rule line against a retention budget of %d = %v, want a *globBudgetError: the flat per-rule charge cannot see a matcher that big", lineBytes, total, err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if budget.ruleBytes < lineBytes {
		t.Fatalf("ruleBytes = %d after refusing a %d-byte rule, want at least its source: the charge ignored the expression the matcher was built from", budget.ruleBytes, lineBytes)
	}
}

// TestLoadIgnoreSetDoesNotRetainZeroRuleMatchers pins both halves of how a
// .gitignore that compiles no rule is handled. It contributes no matcher: such
// a matcher would match nothing, so retaining it changes no answer while
// pinning an ignoreDir and a compiled matcher per rules file — up to the
// listing budget's million for a tree with an empty .gitignore in every
// directory. But reading it is still work the call spends and still source it
// reads, so it is charged for its bytes even though nothing is kept; dropping
// that charge would let a tree of large comment-only files read without bound
// and defeat the aggregate limit.
//
// The returned set is unchanged whether or not the matcher is kept, since a
// matcher with no patterns never excludes anything.
func TestLoadIgnoreSetDoesNotRetainZeroRuleMatchers(t *testing.T) {
	const dirs = 30

	root := t.TempDir()
	var sourceBytes int
	var pathBytes int
	for i := range dirs {
		rel := fmt.Sprintf("d%02d", i)
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Alternate a truly empty file with one holding only a comment and a
		// blank line: both compile no rule.
		content := ""
		if i%2 == 0 {
			content = "# nothing here\n\n"
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sourceBytes += len(content)
		pathBytes += len(rel + "/.gitignore")
	}

	set, budget, err := loadWholeIgnoreSet(t, root)
	if err != nil {
		t.Fatalf("loadIgnoreSet over %d zero-rule .gitignore files: %v", dirs, err)
	}
	if len(set.dirs) != 0 {
		t.Fatalf("retained matchers for %d of %d zero-rule .gitignore files, want 0: each matches nothing, so keeping it only pins memory the budget cannot charge", len(set.dirs), dirs)
	}
	if want := dirs*globIgnoreFileOverheadBytes + pathBytes + sourceBytes; budget.ruleBytes != want {
		t.Fatalf("ruleBytes = %d after reading %d rule-free files totalling %d source bytes and %d path bytes, want %d: each file pays its path charge and the source it read", budget.ruleBytes, dirs, sourceBytes, pathBytes, want)
	}
}

// TestLoadIgnoreSetChargesRuleFreeFilesAgainstTheReadBudget pins the aggregate
// bound on source read. Rule-free files keep no matcher, but the call still
// reads them, so their bytes have to count: otherwise a tree of large
// comment-only .gitignore files is read in full with ruleBytes at zero, and the
// call is bounded only by the listing budget. Every file here is under the
// per-file cap and compiles no rule, so only the call-wide charge can refuse
// the call.
func TestLoadIgnoreSetChargesRuleFreeFilesAgainstTheReadBudget(t *testing.T) {
	const dirs = 20
	const perFile = 8 * 1024
	const total = 32 * 1024 // room for four files, not twenty

	root := t.TempDir()
	for i := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// One comment line only: nothing compiles into a rule.
		content := "# " + strings.Repeat("x", perFile-3) + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubMaxGlobIgnoreTotalBytes(t, total)

	set, budget, err := loadWholeIgnoreSet(t, root)
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("loadIgnoreSet over %d rule-free files of %d bytes each against a read budget of %d = %v, want a *globBudgetError: the source a rule-free file is read from has to count", dirs, perFile, total, err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if len(set.dirs) != 0 {
		t.Fatalf("retained %d matchers for rule-free files, want 0", len(set.dirs))
	}
	if budget.ruleBytes <= total {
		t.Fatalf("ruleBytes = %d, want more than the budget of %d once it refused", budget.ruleBytes, total)
	}
}

// TestLoadIgnoreSetReadsZeroRulePathsOnceAcrossScopes pins that a rule-free
// .gitignore is read once for the whole call, not once per scope. It is
// remembered in the call's dedupe map like any other file — charged its path
// overhead and the source it read — so overlapping scopes neither re-read it
// nor re-charge it, and a large comment-only rules file cannot exhaust the
// aggregate budget through accumulated reads. It still contributes no matcher,
// since a matcher with no patterns matches nothing.
func TestLoadIgnoreSetReadsZeroRulePathsOnceAcrossScopes(t *testing.T) {
	fsys := &openCountingFS{FS: fstest.MapFS{
		".gitignore":   &fstest.MapFile{Data: []byte("# no rules here\n")},
		"a/.gitignore": &fstest.MapFile{Data: []byte("x.log\n")},
		"a/keep.txt":   &fstest.MapFile{Data: []byte("x")},
		"b/keep.txt":   &fstest.MapFile{Data: []byte("x")},
		"c/keep.txt":   &fstest.MapFile{Data: []byte("x")},
	}, opens: map[string]int{}}

	// No scope covers another, so all four survive narrowing and each one
	// reaches the base rules file through its own ancestor read.
	scope := []ignoreScope{
		{prefix: ".", depth: 0, walk: true},
		{prefix: "a", depth: 0, walk: true},
		{prefix: "b", depth: 0, walk: true},
		{prefix: "c", depth: 0, walk: true},
	}
	budget := newGlobBudget("glob")
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, scope)
	if err != nil {
		t.Fatalf("loadIgnoreSet over four overlapping scopes: %v", err)
	}
	if got := fsys.opens[".gitignore"]; got != 1 {
		t.Fatalf("the rule-free base .gitignore was opened %d times across four scopes, want 1: it must be remembered in the dedupe map so no scope re-reads or re-charges it", got)
	}
	if got := fsys.opens["a/.gitignore"]; got != 1 {
		t.Fatalf(".gitignore contributing a rule was opened %d times, want 1", got)
	}
	wantBytes := (globIgnoreFileOverheadBytes + len(".gitignore") + len("# no rules here\n")) +
		(globIgnoreFileOverheadBytes + len("a/.gitignore") + globIgnoreRuleBytes + len("x.log\n"))
	if budget.ruleBytes != wantBytes {
		t.Fatalf("ruleBytes = %d, want %d: each file is charged once for its path, its rules and their source, and the source it read", budget.ruleBytes, wantBytes)
	}
	for _, d := range set.dirs {
		if d.rel == "." {
			t.Fatalf("retained a matcher for the rule-free base .gitignore")
		}
	}
}

// openCountingFS counts how many times Open was called for each name, so a test
// can tell a path the call remembered (opened once) from one it left out of its
// dedupe map (opened again by a later scope).
type openCountingFS struct {
	fs.FS
	opens map[string]int
}

func (o *openCountingFS) Open(name string) (fs.File, error) {
	o.opens[name]++
	return o.FS.Open(name)
}

// TestLoadIgnoreSetMaterializesLinesOnlyAfterTheBudget pins that a file's rule
// lines are collected only once the aggregate budget has accepted the file.
// Materializing a line per line allocates far more memory than the source
// itself, so a file that compiles no rule must never have its lines built, and
// one that would be refused must be refused first. The collection is observed
// through a seam, so the assertion is on the behavior rather than on ambient
// heap allocation.
func TestLoadIgnoreSetMaterializesLinesOnlyAfterTheBudget(t *testing.T) {
	orig := ignoreRuleLines
	t.Cleanup(func() { ignoreRuleLines = orig })
	var collections int
	ignoreRuleLines = func(data []byte) []string {
		collections++
		return orig(data)
	}

	const fileBytes = 1 << 20 // exactly the per-file cap
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), bytes.Repeat([]byte("\n"), fileBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	set, budget, err := loadWholeIgnoreSet(t, root)
	if err != nil {
		t.Fatalf("loadIgnoreSet over a %d-byte newline-heavy .gitignore: %v", fileBytes, err)
	}
	if len(set.dirs) != 0 {
		t.Fatalf("retained %d matchers for a file that compiles no rule, want 0", len(set.dirs))
	}
	if collections != 0 {
		t.Fatalf("collected lines from a %d-byte rule-free .gitignore %d times, want 0: nothing should be materialized for it", fileBytes, collections)
	}
	if want := globIgnoreFileOverheadBytes + len(".gitignore") + fileBytes; budget.ruleBytes != want {
		t.Fatalf("ruleBytes = %d, want %d (path charge plus source read)", budget.ruleBytes, want)
	}

	// A file that would be refused is refused before its lines are collected.
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, ".gitignore"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubMaxGlobIgnoreTotalBytes(t, 1)
	budget2 := newGlobBudget("glob")
	fsys2 := boundedDirFS{FS: os.DirFS(root2), budget: budget2, ctx: t.Context()}
	if _, err := loadIgnoreSet(t.Context(), fsys2, nil, budget2, wholeBaseIgnoreScope()); err == nil {
		t.Fatalf("loadIgnoreSet over a rule file against a 1-byte budget = nil error, want a refusal")
	}
	if collections != 0 {
		t.Fatalf("collected lines from a refused file %d times, want 0", collections)
	}

	// A blank-heavy file with one rule materializes only that rule's line.
	stubMaxGlobIgnoreTotalBytes(t, 1<<20)
	root3 := t.TempDir()
	blankHeavy := "# note\n\n" + strings.Repeat("\n", 4096) + "x.log\n" + strings.Repeat("\n", 4096)
	if err := os.WriteFile(filepath.Join(root3, ".gitignore"), []byte(blankHeavy), 0o644); err != nil {
		t.Fatal(err)
	}
	budget3 := newGlobBudget("glob")
	fsys3 := boundedDirFS{FS: os.DirFS(root3), budget: budget3, ctx: t.Context()}
	set3, err := loadIgnoreSet(t.Context(), fsys3, nil, budget3, wholeBaseIgnoreScope())
	if err != nil {
		t.Fatalf("loadIgnoreSet over a blank-heavy .gitignore with one rule: %v", err)
	}
	if collections != 1 {
		t.Fatalf("collected lines %d times, want 1", collections)
	}
	if len(set3.dirs) != 1 {
		t.Fatalf("retained %d matchers for one rule, want 1", len(set3.dirs))
	}
}

// TestLoadIgnoreSetDoesNotCacheUnreadablePaths pins that a path discovery could
// not read is not remembered. Remembering it would grow the call's dedupe map
// for a file that contributed nothing, and the map is meant to be bounded by
// the charge each remembered path carries. A missing rules file is instead
// retried by a later scope, which costs one failed open and no charge.
func TestLoadIgnoreSetDoesNotCacheUnreadablePaths(t *testing.T) {
	fsys := &openCountingFS{FS: fstest.MapFS{
		"a/keep.txt": &fstest.MapFile{Data: []byte("x")},
		"b/keep.txt": &fstest.MapFile{Data: []byte("x")},
	}, opens: map[string]int{}}
	scope := []ignoreScope{
		{prefix: ".", depth: 0, walk: true},
		{prefix: "a", depth: 0, walk: true},
		{prefix: "b", depth: 0, walk: true},
	}
	budget := newGlobBudget("glob")
	if _, err := loadIgnoreSet(t.Context(), fsys, nil, budget, scope); err != nil {
		t.Fatalf("loadIgnoreSet with no rules files present: %v", err)
	}
	if got := fsys.opens[".gitignore"]; got != 2 {
		t.Fatalf("the missing base .gitignore was opened %d times across two scopes, want 2: an unread path must not be remembered in the dedupe map", got)
	}
	if budget.ruleBytes != 0 {
		t.Fatalf("ruleBytes = %d after only failed reads, want 0: nothing was read or retained", budget.ruleBytes)
	}
}

// TestLoadIgnoreSetAcceptsRepetitionHeavyRulesUnderTheDefaultCap pins that a
// rules file is charged for what it is, not for an estimate of what its rules
// compile into: a few hundred counted-repetition rules, each a legitimate
// pattern, load and exclude their matches instead of refusing the search.
func TestLoadIgnoreSetAcceptsRepetitionHeavyRulesUnderTheDefaultCap(t *testing.T) {
	const rules = 600
	root := t.TempDir()
	writeRootGitignore(t, root, strings.Repeat("a{1000}\n", rules)+"keep.log\n")

	set, _, err := loadWholeIgnoreSet(t, root)
	if err != nil {
		t.Fatalf("loadIgnoreSet over %d repetition rules = %v, want no error", rules, err)
	}
	if !set.matches("keep.log", false) {
		t.Fatal("the rule after the repetition rules was not loaded")
	}
}

// TestRetainIgnoreFileSaturatesInsteadOfWrapping pins that a charge landing on
// an accumulator already near the top of int is refused rather than wrapping to
// a small number that slips under the budget.
func TestRetainIgnoreFileSaturatesInsteadOfWrapping(t *testing.T) {
	stubMaxGlobIgnoreTotalBytes(t, 1<<20)
	budget := newGlobBudget("glob")
	budget.ruleBytes = int(^uint(0) >> 1)
	err := budget.retainIgnoreFile(ignoreFileCost{rules: 1, source: 1})
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("retaining a charge past the top of int = %v, want a *globBudgetError", err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if budget.ruleBytes <= 0 {
		t.Fatalf("ruleBytes = %d after saturation, want a positive saturated value rather than a wrap to a small number", budget.ruleBytes)
	}
	if budget.ruleBytes <= maxGlobIgnoreTotalBytes {
		t.Fatalf("ruleBytes = %d after an overflowing charge, want it above the budget of %d", budget.ruleBytes, maxGlobIgnoreTotalBytes)
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

// TestRetainIgnoreFileChargesEachCost pins what one .gitignore read is billed
// for: a fixed charge plus the path's length for the dedupe map entry, a fixed
// charge per rule line, and the source it read. A file that compiles no rule is
// still charged for its path and source and nothing per rule.
func TestRetainIgnoreFileChargesEachCost(t *testing.T) {
	const total = 64 * 1024
	stubMaxGlobIgnoreTotalBytes(t, total)

	budget := newGlobBudget("glob")
	const source = 32
	const path = 12
	want := globIgnoreFileOverheadBytes + path + globIgnoreRuleBytes + source
	if err := budget.retainIgnoreFile(ignoreFileCost{path: path, rules: 1, source: source}); err != nil {
		t.Fatalf("retaining one rule in a %d-byte file against a budget of %d = %v, want nil", source, total, err)
	}
	if budget.ruleBytes != want {
		t.Fatalf("ruleBytes = %d after one rule, want %d", budget.ruleBytes, want)
	}

	ruleFree := newGlobBudget("glob")
	if err := ruleFree.retainIgnoreFile(ignoreFileCost{path: path, source: source}); err != nil {
		t.Fatalf("retaining a rule-free file of %d source bytes = %v, want nil", source, err)
	}
	if wantFree := globIgnoreFileOverheadBytes + path + source; ruleFree.ruleBytes != wantFree {
		t.Fatalf("ruleBytes = %d for a rule-free file, want %d (path and source, no rule charge)", ruleFree.ruleBytes, wantFree)
	}

	// Enough rule lines cross a budget their source alone would fit.
	many := newGlobBudget("glob")
	const rules = 64
	smallTotal := globIgnoreFileOverheadBytes + rules*globIgnoreRuleBytes - 1
	stubMaxGlobIgnoreTotalBytes(t, smallTotal)
	err := many.retainIgnoreFile(ignoreFileCost{rules: rules, source: rules})
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("retaining %d rules against a budget of %d = %v, want a *globBudgetError: each rule must be charged", rules, smallTotal, err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
}

// TestCompiledIgnoreRulesMatchesGoGitignoreSkips pins the rule count the budget
// charges for a .gitignore's bytes. go-gitignore drops a line only when it is a
// comment in column zero or, after trimming spaces from both ends, is empty;
// every other line it tries to compile. The count is an UPPER BOUND on the
// retained patterns, not an exact match: a line whose generated regexp fails to
// compile (an unterminated character class, say) is still counted. Over-counting
// is the safe direction: it can refuse a call early but never under-charge one.
//
// The trailing check ties the bound to observable behavior rather than the
// library's internals: an ordinary rule excludes its path, while an
// uncompilable line still counts as the upper bound and excludes nothing.
func TestCompiledIgnoreRulesMatchesGoGitignoreSkips(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lines     []string
		wantRules int
	}{
		{"ordinary rules", []string{"a", "b"}, 2},
		{"blank lines", []string{"", "a", ""}, 1},
		{"space-only line", []string{"   ", "a"}, 1},
		{"comment in column zero", []string{"# note", "a"}, 1},
		{"indented comment still compiles", []string{"   # note"}, 1},
		{"negated rule", []string{"!keep", "a"}, 2},
		{"bare carriage return line", []string{"  \r", "a"}, 1},
		{"uncompilable pattern", []string{"["}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost := compiledIgnoreRules([]byte(strings.Join(tc.lines, "\n")))
			if cost.rules != tc.wantRules {
				t.Fatalf("compiledIgnoreRules(%q) rules = %d, want %d", tc.lines, cost.rules, tc.wantRules)
			}
		})
	}

	lib := gitignore.CompileIgnoreLines(strings.Split("[\na\n", "\n")...)
	if !lib.MatchesPath("a") {
		t.Fatalf("go-gitignore did not exclude a path the ordinary rule beside an uncompilable line names; the counter's model of a rule line is wrong")
	}
	if lib.MatchesPath("keep") {
		t.Fatalf("go-gitignore excluded a path no compiled line names")
	}
}

// TestLoadIgnoreSetSkipsADotDirectoryAncestor pins that the ancestor read
// applies the dot-directory exclusion the subtree walk applies. IsDotPath
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

// TestLoadIgnoreSetSkipsAScopeBeneathADotDirectory pins the mid-path half of
// the dot-directory exclusion: a scope like a/.config/sub sits beneath a
// dot-directory without naming one itself, so neither the basename ancestor
// check nor the walk's own d.Name() check fires. IsDotPath still drops every
// candidate beneath .config before a rule from inside could apply, so walking
// the scope and retaining its rules only spends the listing and rules budget
// for nothing — and a large enough subtree under it can refuse a glob whose
// answer is fixed (empty under the default exclusion) before it starts.
func TestLoadIgnoreSetSkipsAScopeBeneathADotDirectory(t *testing.T) {
	fsys := fstest.MapFS{
		"a/.config/sub/.gitignore": &fstest.MapFile{Data: []byte("*.log\n")},
		"a/.config/sub/keep.txt":   &fstest.MapFile{Data: []byte("x")},
		"a/keep2.txt":              &fstest.MapFile{Data: []byte("x")},
		".gitignore":               &fstest.MapFile{Data: []byte("root.log\n")},
	}

	budget := newGlobBudget("glob")
	set, err := loadIgnoreSet(t.Context(), fsys, nil, budget, []ignoreScope{{prefix: "a/.config/sub", depth: 0, walk: true}})
	if err != nil {
		t.Fatalf("loadIgnoreSet scoped beneath a dot-directory: %v", err)
	}
	for _, d := range set.dirs {
		if d.rel == "a/.config/sub" || d.rel == "a/.config" {
			t.Fatalf("collected rules from %q beneath a dot-directory; every candidate below it is already dropped, so reading them only spends budget", d.rel)
		}
	}
	if budget.listings != 0 {
		t.Fatalf("discovery spent %d listings walking beneath a dot-directory whose candidates are all dropped", budget.listings)
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
