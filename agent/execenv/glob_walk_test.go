package execenv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

	w := &globWalkFS{FS: tree, ctx: t.Context(), listed: map[string]fs.FileInfo{}}
	if _, err := w.ReadDir("dir"); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	w.unidentified = maxUnidentifiedGlobDirs
	_, err := w.ReadDir("dir")
	if err == nil {
		t.Fatal("listing past the bound succeeded, want a refusal")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("listing past the bound reported %v, which the walk skips silently; it must fail the glob", err)
	}
}

// TestGlobWalkFSKnowsWhenIdentityIsAvailable pins the discrimination the
// backstop turns on: an os-backed filesystem carries file identity and is
// bounded by the cycle check, so it must never be counted against the bound.
func TestGlobWalkFSKnowsWhenIdentityIsAvailable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &globWalkFS{FS: os.DirFS(root), ctx: t.Context(), listed: map[string]fs.FileInfo{}}
	if _, err := w.ReadDir("sub"); err != nil {
		t.Fatalf("listing an os-backed directory: %v", err)
	}
	if w.unidentified != 0 {
		t.Fatalf("os-backed listing counted %d unidentified directories, want 0", w.unidentified)
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
