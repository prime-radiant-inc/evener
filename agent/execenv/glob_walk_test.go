package execenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// globLoopFixture builds a tree holding one real match plus a directory
// symlink that points back at the tree root — the shape /proc/<pid>/root has,
// where following the link re-enters the tree it lives in. It returns the
// fixture root and the absolute path of the one file a `**` glob should match.
func globLoopFixture(t *testing.T) (root, wantMatch string) {
	t.Helper()
	root = t.TempDir()
	nest := filepath.Join(root, "deep", "nest")
	if err := os.MkdirAll(nest, 0o755); err != nil {
		t.Fatal(err)
	}
	wantMatch = filepath.Join(nest, "needle.txt")
	if err := os.WriteFile(wantMatch, []byte("found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "deep", "loop")); err != nil {
		t.Skipf("directory symlinks unavailable on this platform: %v", err)
	}
	return root, wantMatch
}

// globAliasFixture builds the shape this repo's own fleet layout documents:
// one real directory of sources reachable under a second name through a
// directory symlink (node_modules/lib -> ../packages/lib). The link points
// sideways, never at one of its own ancestors, so nothing here is a cycle and
// every source file must be reported under both names.
func globAliasFixture(t *testing.T) (root string, wantBothPaths []string) {
	t.Helper()
	root = t.TempDir()
	sources := filepath.Join(root, "packages", "lib")
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.ts", "b.ts"} {
		if err := os.WriteFile(filepath.Join(sources, name), []byte("export {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "packages", "lib"), filepath.Join(root, "node_modules", "lib")); err != nil {
		t.Skipf("directory symlinks unavailable on this platform: %v", err)
	}
	return root, []string{
		filepath.Join(root, "node_modules", "lib", "a.ts"),
		filepath.Join(root, "node_modules", "lib", "b.ts"),
		filepath.Join(root, "packages", "lib", "a.ts"),
		filepath.Join(root, "packages", "lib", "b.ts"),
	}
}

// sortedCopy returns paths in lexical order, so a test can compare match sets
// without depending on the newest-mtime-first order Glob returns them in.
func sortedCopy(paths []string) []string {
	out := slices.Clone(paths)
	slices.Sort(out)
	return out
}

// TestGlobDoesNotTraverseDirectorySymlinkLoops is the unit-scale reproduction
// of #369: find_files with a `**` pattern rooted at / never returned because
// the walk followed /proc/<pid>/root back to / and recursed. The glob must
// terminate and must report the single real match, not the copies reachable
// through the loop.
func TestGlobDoesNotTraverseDirectorySymlinkLoops(t *testing.T) {
	root, wantMatch := globLoopFixture(t)

	// t.Context() is cancelled when the test ends, so a walk that ignores the
	// loop bound unwinds instead of spinning on past the failure.
	ctx := t.Context()

	type outcome struct {
		matches []string
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		matches, err := NewLocalExecutionEnvironment(root).Glob(ctx, "**/needle.txt", root, true)
		done <- outcome{matches, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Glob over a symlink loop: %v", got.err)
		}
		if want := []string{wantMatch}; !reflect.DeepEqual(got.matches, want) {
			t.Fatalf("Glob over a symlink loop = %v, want %v (the loop must not be traversed)", got.matches, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Glob over a directory symlink loop did not terminate")
	}
}

// TestGlobReportsMatchesThroughANonCyclicDirectorySymlink pins the cost of
// getting termination wrong: refusing every directory symlink (rather than
// only the ones that re-enter the walk) silently drops every file reachable
// through a node_modules-style link, which is exactly how this repo's own
// fleets are laid out. Both names for the same sources must match.
func TestGlobReportsMatchesThroughANonCyclicDirectorySymlink(t *testing.T) {
	root, want := globAliasFixture(t)

	got, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/*.ts", root)
	if err != nil {
		t.Fatalf("Glob over an aliased directory: %v", err)
	}
	if gotSorted := sortedCopy(got); !reflect.DeepEqual(gotSorted, want) {
		t.Fatalf("Glob(**/*.ts) = %v, want %v (matches through the symlinked name must not be dropped)", gotSorted, want)
	}
}

// TestGlobLiteralAndWildcardSegmentsAgreeThroughASymlink covers the
// self-inconsistency the same over-broad rule produced: doublestar never
// consults directory-ness for the meta-free prefix of a pattern, so a
// literally-spelled symlinked directory was traversed while a wildcard segment
// naming that same directory was not. Spelling must not change the answer.
func TestGlobLiteralAndWildcardSegmentsAgreeThroughASymlink(t *testing.T) {
	root, _ := globAliasFixture(t)
	env := NewLocalExecutionEnvironment(root)

	literal, err := env.Glob(t.Context(), "node_modules/lib/*.ts", root)
	if err != nil {
		t.Fatalf("Glob with a literal symlinked segment: %v", err)
	}
	wildcard, err := env.Glob(t.Context(), "node_modules/*/*.ts", root)
	if err != nil {
		t.Fatalf("Glob with a wildcard segment: %v", err)
	}
	if len(literal) == 0 {
		t.Fatalf("Glob(node_modules/lib/*.ts) = %v, want the two sources behind the link", literal)
	}
	if !reflect.DeepEqual(sortedCopy(literal), sortedCopy(wildcard)) {
		t.Fatalf("Glob(node_modules/lib/*.ts) = %v but Glob(node_modules/*/*.ts) = %v; the two spellings must agree",
			sortedCopy(literal), sortedCopy(wildcard))
	}
}

// TestGlobWalkFSBoundsAFilesystemWithoutFileIdentity covers the backstop for
// the case cycle detection cannot serve: a filesystem whose entries carry no
// (device, inode) identity to compare. The walk is bounded by how many
// listings it makes there, and running out says so rather than handing back
// the short list it happened to collect.
func TestGlobWalkFSBoundsAFilesystemWithoutFileIdentity(t *testing.T) {
	tree := fstest.MapFS{"dir/file.txt": &fstest.MapFile{Data: []byte("x")}}
	if info, err := fs.Stat(tree, "dir"); err != nil || hasFileIdentity(info) {
		t.Fatalf("fstest.MapFS should carry no file identity: (%v, %v)", info, err)
	}

	w := &globWalkFS{FS: tree, ctx: t.Context(), budget: newGlobBudget("glob")}
	if _, err := w.ReadDir("dir"); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	w.budget.listings = maxGlobDirListings
	_, err := w.ReadDir("dir")
	if err == nil {
		t.Fatal("listing past the bound succeeded, want a refusal")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("listing past the bound reported %v, which the walk skips silently; it must fail the glob", err)
	}
}

// TestGlobWalkFSKnowsWhenIdentityIsAvailable pins the discrimination the
// backstop turns on: identity decides how running out of budget is EXPLAINED,
// not whether a listing counts toward it. An os-backed filesystem is bounded
// by the cycle check too, so its listings are charged to the budget exactly
// like an identity-less filesystem's — only the message a refusal gives once
// the bound trips differs.
func TestGlobWalkFSKnowsWhenIdentityIsAvailable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), budget: newGlobBudget("glob")}
	if _, err := w.ReadDir("sub"); err != nil {
		t.Fatalf("listing an os-backed directory: %v", err)
	}
	if w.budget.listings != 1 {
		t.Fatalf("os-backed listing charged %d to the budget, want 1", w.budget.listings)
	}
	info, err := fs.Stat(w.FS, "sub")
	if err != nil {
		t.Fatalf("stat sub: %v", err)
	}
	if !hasFileIdentity(info) {
		t.Fatalf("an os-backed directory must carry file identity")
	}
}

// TestSortPathsByMtimeDescStatsEachPathOnce guards the post-walk phase against
// the shape it had before: two Stat calls inside the sort comparator, so an
// n-path result cost O(n log n) stats and a large one spent seconds in an
// uninterruptible sort. Decorating once means exactly one stat per path.
func TestSortPathsByMtimeDescStatsEachPathOnce(t *testing.T) {
	paths := mtimeFixture(t, 8)

	counts := map[string]int{}
	stubSortPathStat(t, func(p string) { counts[p]++ })

	if err := sortPathsByMtimeDesc(t.Context(), paths); err != nil {
		t.Fatalf("sortPathsByMtimeDesc: %v", err)
	}
	if len(counts) != len(paths) {
		t.Fatalf("stat'ed %d distinct paths, want %d", len(counts), len(paths))
	}
	for p, n := range counts {
		if n != 1 {
			t.Errorf("stat(%s) called %d times, want 1", p, n)
		}
	}
}

// TestSortPathsByMtimeDescOrdersNewestFirst pins the ordering contract the
// decorate-sort-undecorate rewrite has to preserve.
func TestSortPathsByMtimeDescOrdersNewestFirst(t *testing.T) {
	paths := mtimeFixture(t, 4)
	// Age them in reverse: paths[0] oldest ... paths[3] newest.
	base := time.Now().Add(-time.Hour)
	for i, p := range paths {
		when := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	shuffled := []string{paths[1], paths[3], paths[0], paths[2]}
	if err := sortPathsByMtimeDesc(t.Context(), shuffled); err != nil {
		t.Fatalf("sortPathsByMtimeDesc: %v", err)
	}
	want := []string{paths[3], paths[2], paths[1], paths[0]}
	if !reflect.DeepEqual(shuffled, want) {
		t.Fatalf("sortPathsByMtimeDesc = %v, want newest first %v", shuffled, want)
	}
}

// stubSortPathStat interposes on the stat seam sortPathsByMtimeDesc uses,
// reporting every path it stats to observe before delegating to the real stat,
// and restores the seam when the test ends.
func stubSortPathStat(t *testing.T, observe func(path string)) {
	t.Helper()
	orig := sortPathStat
	sortPathStat = func(p string) (os.FileInfo, error) {
		observe(p)
		return orig(p)
	}
	t.Cleanup(func() { sortPathStat = orig })
}

// mtimeFixture creates n files and returns their absolute paths.
func mtimeFixture(t *testing.T, n int) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 0, n)
	for i := range n {
		p := filepath.Join(dir, string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// listCountingFS counts every directory listing a walk makes of it, so a test
// can assert on how much walking a pattern costs rather than only on what it
// found. A listing count is the resource the shared-walk change exists to spend
// once instead of once per brace alternative, and unlike elapsed time it does
// not depend on host load.
type listCountingFS struct {
	fs.FS
	dirs []string
}

func (c *listCountingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	c.dirs = append(c.dirs, name)
	return fs.ReadDir(c.FS, name)
}

func (c *listCountingFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(c.FS, name) }

// globBraceTree builds the shape a brace expansion's alternatives overlap on: a
// tree several levels deep whose files sort into more than one extension group,
// nested .gitignore files carrying an exclusion and a negation, and a dot
// directory the default exclusion drops.
func globBraceTree(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, data string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.log\n!keep.log\n")
	for _, pkg := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"} {
		write(pkg+"/a.go", "package a\n")
		write(pkg+"/a.md", "doc\n")
		write(pkg+"/sub/b.go", "package b\n")
		write(pkg+"/sub/b.md", "doc\n")
		write(pkg+"/sub/deep/c.go", "package c\n")
		write(pkg+"/skip.log", "log\n")
		write(pkg+"/keep.log", "keep\n")
		write(pkg+"/.hidden/h.go", "package h\n")
		if pkg == "alpha" || pkg == "beta" {
			write(pkg+"/.gitignore", "*.md\n!README.md\n")
			write(pkg+"/README.md", "readme\n")
		}
	}
	write("top.go", "package top\n")
	write("top.md", "doc\n")
	return root
}

// globPerPatternResult is the answer the per-pattern loop produced before the
// walk was shared: each expanded pattern walked separately, its matches
// filtered and counted one at a time, with duplicates dropped after filtering.
// The shared walk has to reproduce it exactly, so these tests compare against it
// rather than against hand-written expectations.
func globPerPatternResult(t *testing.T, root, pattern string, includeIgnored bool) (matches []string, excluded int) {
	t.Helper()
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		t.Fatalf("expandSearchPattern(%q): %v", pattern, err)
	}
	budget := NewGlobBudget()
	fsys := cancelFS{ctx: t.Context(), fsys: globBaseFS(t.Context(), root, budget)}
	var ignores *ignoreSet
	if !includeIgnored {
		ignores, err = loadIgnoreSet(t.Context(), fsys, nil, budget, ignoreScopeForPatterns(patterns))
		if err != nil {
			t.Fatalf("loadIgnoreSet: %v", err)
		}
	}
	seen := make(map[string]struct{})
	for _, p := range patterns {
		found, merr := globMatches(t.Context(), fsys, p, budget)
		if merr != nil {
			t.Fatalf("globMatches(%q): %v", p, merr)
		}
		for _, m := range found {
			if !includeIgnored {
				drop, derr := globMatchExcluded(t.Context(), fsys, ignores, m)
				if derr != nil {
					t.Fatalf("globMatchExcluded(%q): %v", m, derr)
				}
				if drop {
					excluded++
					continue
				}
			}
			abs := filepath.Join(root, m)
			if _, ok := seen[abs]; ok {
				continue
			}
			seen[abs] = struct{}{}
			matches = append(matches, abs)
		}
	}
	if err := sortPathsByMtimeDesc(t.Context(), matches); err != nil {
		t.Fatalf("sortPathsByMtimeDesc: %v", err)
	}
	return matches, excluded
}

// TestGlobBraceExpansionMatchesEveryAlternativeIdentically is the equivalence
// half of the shared-walk change: a brace pattern must report exactly the paths,
// exclusion count and order the per-pattern loop reported, whether or not its
// alternatives overlap.
//
// The table's axis is why a pattern does or does not share its walk. Two
// recursive alternatives under one prefix are the case the shared walk
// collapses; alternatives with different roots, a selective first component,
// one literal term, or distinct last components each exercise a different reason
// a pattern keeps its own walk, and all of them must still agree.
func TestGlobBraceExpansionMatchesEveryAlternativeIdentically(t *testing.T) {
	root := globBraceTree(t)
	env := NewLocalExecutionEnvironment(root)

	for _, tc := range []struct {
		name    string
		pattern string
	}{
		{"overlapping recursive alternatives", "{alpha,beta}/**/*.{go,md}"},
		{"one prefix two extensions", "alpha/**/*.{go,md}"},
		{"three extensions one prefix", "alpha/**/*.{go,md,log}"},
		{"alternatives under different roots", "{top.go,top.md}"},
		{"recursive and literal alternatives", "{**/*.go,top.md}"},
		{"selective first component", "alpha/{sub,deep}/**/*.go"},
		{"alternative naming a directory", "alpha/**/{sub,deep}"},
		{"single alternative", "alpha/**/*.go"},
		{"no alternatives at all", "alpha/**/*"},
		{"trailing separator alternative", "{alpha/,beta/}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, includeIgnored := range []bool{false, true} {
				want, wantExcluded := globPerPatternResult(t, root, tc.pattern, includeIgnored)
				got, gotExcluded, err := env.GlobWithExclusions(t.Context(), tc.pattern, root, includeIgnored)
				if err != nil {
					t.Fatalf("GlobWithExclusions(%q, includeIgnored=%v): %v", tc.pattern, includeIgnored, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("GlobWithExclusions(%q, includeIgnored=%v) = %v, want %v (the per-pattern answer it must reproduce)", tc.pattern, includeIgnored, got, want)
				}
				if gotExcluded != wantExcluded {
					t.Fatalf("GlobWithExclusions(%q, includeIgnored=%v) excluded = %d, want %d (one exclusion per pattern that selected the path)", tc.pattern, includeIgnored, gotExcluded, wantExcluded)
				}
			}
		})
	}
}

// TestGlobBraceExpansionListsOverlappingSubtreesOnce is the work half: two
// recursive alternatives sharing a prefix must cost one traversal of that
// subtree, not one each. The bound is expressed against the single-pattern walk
// over the same tree rather than against a constant, so it stays meaningful if
// the walk itself changes shape.
func TestGlobBraceExpansionListsOverlappingSubtreesOnce(t *testing.T) {
	root := globBraceTree(t)
	env := NewLocalExecutionEnvironment(root)

	listings := func(pattern string) int {
		t.Helper()
		var counter *listCountingFS
		stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
			counter = &listCountingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}}
			return counter
		})
		if _, _, err := env.GlobWithBudget(t.Context(), pattern, root, true, NewGlobBudget()); err != nil {
			t.Fatalf("GlobWithBudget(%q): %v", pattern, err)
		}
		return len(counter.dirs)
	}

	single := listings("alpha/**/*.go")
	if single == 0 {
		t.Fatal("the single-pattern control made no listings; the fixture proves nothing")
	}
	// doublestar lists a directory more than once for some pattern shapes (a
	// `**` component is entered through two code paths), so the exact number is
	// its business. The contract is the direction: the brace call may not cost
	// more than one alternative's own walk, where walking each alternative
	// separately cost their sum.
	brace := listings("alpha/**/*.{go,md}")
	if brace > single {
		t.Fatalf("brace glob over one prefix made %d directory listings, want at most %d, the one-alternative walk it replaces", brace, single)
	}
	if separate := brace + listings("alpha/**/*.md"); separate <= brace {
		t.Fatalf("the two alternatives walked separately cost %d listings, want more than the shared walk's %d; the control proves nothing", separate, brace)
	}

	// Two prefixes are two subtrees, so two walks; each is still walked once
	// rather than once per alternative.
	wantTwo := single + listings("beta/**/*.go")
	if twoRoots := listings("{alpha,beta}/**/*.go"); twoRoots > wantTwo {
		t.Fatalf("brace glob over two prefixes made %d directory listings, want at most %d (one traversal per prefix)", twoRoots, wantTwo)
	}
}

// TestGlobBraceExpansionKeepsItsMatchSetIdentical proves the collapse does not
// change an answer over a real tree with nested .gitignore files, negations and
// dot directories — this repo's own worktree. It compares against the
// per-pattern loop on the same tree, so it catches a shared walk that listed a
// subtree an alternative's own walk never reached, or that dropped a rule's
// effect on one alternative's matches only.
func TestGlobBraceExpansionKeepsItsMatchSetIdentical(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "agent", "execenv")); err != nil {
		t.Skipf("repo worktree not present: %v", err)
	}
	env := NewLocalExecutionEnvironment(repoRoot)

	for _, pattern := range []string{
		"agent/**/*.{go,md}",
		"agent/**/*.{go,_test.go}",
		"docs/**/*.{go,md}",
		"agent/{execenv,doctor}/**/*.go",
	} {
		for _, includeIgnored := range []bool{false, true} {
			want, wantExcluded := globPerPatternResult(t, repoRoot, pattern, includeIgnored)
			got, gotExcluded, gerr := env.GlobWithExclusions(t.Context(), pattern, repoRoot, includeIgnored)
			if gerr != nil {
				t.Fatalf("GlobWithExclusions(%q): %v", pattern, gerr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("GlobWithExclusions(%q, includeIgnored=%v) returned %d paths, want the per-pattern answer's %d", pattern, includeIgnored, len(got), len(want))
			}
			if gotExcluded != wantExcluded {
				t.Fatalf("GlobWithExclusions(%q, includeIgnored=%v) excluded = %d, want %d", pattern, includeIgnored, gotExcluded, wantExcluded)
			}
		}
	}
}

// TestGlobSharedWalkRefusesWhenTheBudgetRunsOut pins the hazard a shared walk
// creates: one traversal serving several alternatives must still honour the
// call's listing budget, and must refuse rather than hand back the subtree it
// managed to list. A shared walk that quietly stopped listing would return a
// plausible-looking short answer for every alternative at once, which is exactly
// the silent-empty answer the budget exists to prevent.
func TestGlobSharedWalkRefusesWhenTheBudgetRunsOut(t *testing.T) {
	root := globBraceTree(t)
	env := NewLocalExecutionEnvironment(root)

	// Size the budget below what the call actually needs, measured rather than
	// guessed, so the refusal is provoked by the walk rather than by the
	// fixture happening to be shallow.
	full := 0
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		return countingReadDirFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}, n: &full}
	})
	if _, _, err := env.GlobWithBudget(t.Context(), "alpha/**/*.{go,md}", root, true, NewGlobBudget()); err != nil {
		t.Fatalf("control run: %v", err)
	}
	if full < 3 {
		t.Fatalf("the control call made %d listings; too shallow to provoke a refusal", full)
	}
	stubMaxGlobDirListings(t, full/2)

	matches, _, err := env.GlobWithBudget(t.Context(), "alpha/**/*.{go,md}", root, true, NewGlobBudget())
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("shared brace walk needing %d listings against a budget of %d = (%d matches, %v), want a budget refusal", full, full/2, len(matches), err)
	}
	if budgetErr.kind != budgetListings {
		t.Fatalf("refusal kind = %v, want budgetListings", budgetErr.kind)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the budget refusal came back as %v, which a walk skips silently", err)
	}
}

// TestGlobSharedWalkSharesTheMatchCapAcrossAlternatives pins that a shared walk
// charges the call's match cap once per candidate path rather than once per
// alternative, and so shares one cap across the alternatives exactly as the
// per-pattern loop did.
func TestGlobSharedWalkSharesTheMatchCapAcrossAlternatives(t *testing.T) {
	root := globBraceTree(t)
	env := NewLocalExecutionEnvironment(root)

	stubMaxGlobMatches(t, 3)
	budget := NewGlobBudget()
	got, _, err := env.GlobWithBudget(t.Context(), "alpha/**/*.{go,md}", root, true, budget)
	if err != nil {
		t.Fatalf("GlobWithBudget: %v", err)
	}
	if budget.TruncatedAt() != 3 {
		t.Fatalf("truncatedAt = %d, want 3 (the call-wide cap)", budget.TruncatedAt())
	}
	if len(got) == 0 {
		t.Fatal("returned no matches against a cap of 3; the cap must still admit the candidates it charged")
	}
	if len(got) > 3 {
		t.Fatalf("returned %d matches against a cap of 3", len(got))
	}
}

// TestGlobBraceExpansionNeverListsASubtreeItsPatternsCannotReach pins that
// grouping is by literal prefix plus a **-rooted remainder: a pattern whose
// first component after the prefix is selective keeps its own walk, so a shared
// cover never lists a directory that pattern's own walk would leave out. The
// fixture has a large sibling directory the pattern cannot match inside, which
// is what a too-eager cover would walk into.
func TestGlobBraceExpansionNeverListsASubtreeItsPatternsCannotReach(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "huge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(root, "sub", "huge", fmt.Sprintf("bulk%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var listed []string
	stubGlobBaseFS(t, func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
		return &recordingFS{
			listCountingFS: &listCountingFS{FS: boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}},
			listed:         &listed,
		}
	})
	if _, _, err := NewLocalExecutionEnvironment(root).GlobWithBudget(t.Context(), "sub/*.{txt,md}", root, true, NewGlobBudget()); err != nil {
		t.Fatalf("GlobWithBudget: %v", err)
	}
	if len(listed) == 0 {
		t.Fatal("no directory was listed; the fixture proves nothing")
	}
	for _, dir := range listed {
		if strings.HasPrefix(dir, "sub/huge") {
			t.Fatalf("the walk listed %q; a pattern whose remainder holds no ** never reaches that subtree, so no shared cover may list it either", dir)
		}
	}
}

// recordingFS is a listCountingFS that also records every listed directory name,
// so a test can assert on which directories a walk entered rather than only how
// many.
type recordingFS struct {
	*listCountingFS
	listed *[]string
}

func (r *recordingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	*r.listed = append(*r.listed, name)
	return r.listCountingFS.ReadDir(name)
}

func (r *recordingFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(r.FS, name) }
