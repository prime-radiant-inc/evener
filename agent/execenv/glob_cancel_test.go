package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"primeradiant.com/evener/agent/sandbox"
)

// countingFS counts the ReadDir calls a walk makes and, when cancel is set,
// cancels the walk's context on the cancelOn'th one.
//
// It must sit OUTSIDE the ctx-observing wrapper: a counter placed inside it
// stops being reached the moment the wrapper starts short-circuiting, so it
// can never show whether the walk kept going after the cancellation — which is
// exactly the question a promptness assertion asks.
type countingFS struct {
	fs.FS
	cancelOn int
	cancel   context.CancelFunc
	calls    int
}

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	c.calls++
	if c.cancel != nil && c.calls == c.cancelOn {
		c.cancel()
	}
	return fs.ReadDir(c.FS, name)
}

func (c *countingFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(c.FS, name)
}

// globCancelTree is a 24-file tree deep enough that a completed `**` walk
// takes many more ReadDir calls than an aborted one.
func globCancelTree() fstest.MapFS {
	tree := fstest.MapFS{}
	for _, dir := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		for _, sub := range []string{"one", "two", "three"} {
			tree[filepath.Join(dir, sub, "needle.txt")] = &fstest.MapFile{Data: []byte("x")}
		}
	}
	return tree
}

// TestGlobMatchesStopsOnContextCancellation proves the other half of #369: an
// in-flight glob must abort when its context is cancelled and report the
// cancellation, so job_stop and session teardown can actually stop a walk
// instead of only asking. doublestar swallows I/O errors, so a cancelled walk
// would otherwise come back as a short result with a nil error.
//
// The promptness half is measured against a completed walk over the same tree
// rather than a hand-picked constant, so the bound stays meaningful if
// doublestar's traversal changes shape.
func TestGlobMatchesStopsOnContextCancellation(t *testing.T) {
	tree := globCancelTree()

	full := &countingFS{FS: cancelFS{ctx: t.Context(), fsys: tree}}
	if _, err := globMatches(t.Context(), full, "**/needle.txt", newGlobBudget("glob")); err != nil {
		t.Fatalf("uncancelled globMatches: %v", err)
	}
	if full.calls < 20 {
		t.Fatalf("completed walk took only %d ReadDir calls; the fixture is too small to measure promptness", full.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counter := &countingFS{FS: cancelFS{ctx: ctx, fsys: tree}, cancelOn: 3, cancel: cancel}

	matches, err := globMatches(ctx, counter, "**/needle.txt", newGlobBudget("glob"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globMatches after mid-walk cancellation = (%v, %v), want context.Canceled", matches, err)
	}
	if matches != nil {
		t.Fatalf("globMatches after cancellation returned partial matches %v, want none", matches)
	}
	// The walk must unwind at the cancelled call, not grind through the
	// siblings its parent frames already listed. One extra call is slack for
	// the frame that observes the cancellation.
	if counter.calls > counter.cancelOn+1 {
		t.Fatalf("walk kept reading after cancellation: %d ReadDir calls (a completed walk takes %d)", counter.calls, full.calls)
	}
}

// TestGlobMatchesSurvivesAnUnreadableDirectory pins the property that makes
// the abort safe to build on: aborting on the cancellation must not turn every
// other I/O failure into a failed glob. A directory the walk cannot read is
// still skipped, and the matches around it are still returned.
func TestGlobMatchesSurvivesAnUnreadableDirectory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"readable", "locked"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "needle.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("this user can read a 0000 directory; cannot stage an unreadable directory")
	}

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), "**/needle.txt", root, true)
	if err != nil {
		t.Fatalf("Glob past an unreadable directory: %v", err)
	}
	want := []string{filepath.Join(root, "readable", "needle.txt")}
	if len(matches) != 1 || matches[0] != want[0] {
		t.Fatalf("Glob past an unreadable directory = %v, want %v", matches, want)
	}
}

// TestGlobWithExclusionsReportsCancellation checks the contract through the
// exported entry point the find_files tool calls, so the cancellation is
// surfaced to the tool rather than being flattened into an empty result.
func TestGlobWithExclusionsReportsCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(ctx, "**/needle.txt", root, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GlobWithExclusions with a cancelled context = (%v, %d, %v), want context.Canceled", matches, excluded, err)
	}
}

// TestGlobWithExclusionsStopsMidWalk is the exported-entry-point version of
// the contract: a walk cancelled once it is already under way must come back
// as a cancellation, not as the partial list it had collected by then.
//
// Promptness is not measured here and cannot be: the counter reaches the walk
// only from inside GlobWithExclusions' own ctx wrapper, which short-circuits
// before delegating, so post-cancellation calls never reach it. That
// measurement lives in TestGlobMatchesStopsOnContextCancellation, where the
// test owns the layering.
func TestGlobWithExclusionsStopsMidWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stubGlobBaseFS(t, func(context.Context, string, *GlobBudget) fs.FS {
		return &countingFS{FS: globCancelTree(), cancelOn: 3, cancel: cancel}
	})

	root := t.TempDir()
	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(ctx, "**/needle.txt", root, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GlobWithExclusions cancelled mid-walk = (%v, %d, %v), want context.Canceled", matches, excluded, err)
	}
	if matches != nil {
		t.Fatalf("GlobWithExclusions cancelled mid-walk returned partial matches %v, want none", matches)
	}
}

// TestLoadIgnoreSetReportsCancellation pins the discovery half of the
// cancellation contract: a discovery walk over an already-cancelled context
// reports the cancellation rather than a partial (here empty) rule set with a
// nil error. Without it a default glob or grep keeps traversing after
// cancellation and only surfaces the cancellation once discovery finishes —
// or never, if the walk completes while cancelled.
func TestLoadIgnoreSetReportsCancellation(t *testing.T) {
	tree := fstest.MapFS{
		"a/b/.gitignore": &fstest.MapFile{Data: []byte("*.log\n")},
		"a/b/keep.txt":   &fstest.MapFile{Data: []byte("x")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	set, err := loadIgnoreSet(ctx, cancelFS{ctx: ctx, fsys: tree}, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadIgnoreSet with a cancelled context = (%v, %v), want context.Canceled", set, err)
	}
}

// cancelOnDirReadFS fires its cancel the first time ReadDir succeeds for dir,
// so entries already read keep flowing through the walk callback with a nil
// error — the shape a cancellation landing mid-directory takes.
type cancelOnDirReadFS struct {
	fs.FS
	cancel context.CancelFunc
	dir    string
	fired  bool
}

func (c *cancelOnDirReadFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(c.FS, name)
	if err == nil && !c.fired && name == c.dir {
		c.fired = true
		c.cancel()
	}
	return entries, nil
}

// TestLoadIgnoreSetStopsBetweenEntries pins the promptness half the error-path
// check cannot see: a cancellation landing after a directory was successfully
// read keeps delivering that directory's remaining entries with a nil error,
// and no file is ever opened again, so nothing observes the cancellation. The
// walk must still report it rather than a partial rule set with a nil error.
func TestLoadIgnoreSetStopsBetweenEntries(t *testing.T) {
	tree := fstest.MapFS{".gitignore": &fstest.MapFile{Data: []byte("root.log\n")}}
	for i := range 200 {
		name := "d/f" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".txt"
		tree[name] = &fstest.MapFile{Data: []byte("x")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fsys := cancelFS{ctx: ctx, fsys: &cancelOnDirReadFS{FS: tree, cancel: cancel, dir: "d"}}
	set, err := loadIgnoreSet(ctx, fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadIgnoreSet cancelled mid-directory = (%v, %v), want context.Canceled", set, err)
	}
}

// TestLoadIgnoreSetStopsMidWalkCancels is the promptness half: a cancellation
// landing once discovery is already under way must unwind the walk, not grind
// through the rest of the tree. The counter cancels on the second listing, so
// a walk that ignores the cancellation visits every directory while one that
// propagates it stops within about one more call.
func TestLoadIgnoreSetStopsMidWalkCancels(t *testing.T) {
	tree := globCancelTree()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counter := &countingFS{FS: tree, cancelOn: 2, cancel: cancel}
	_, err := loadIgnoreSet(ctx, cancelFS{ctx: ctx, fsys: counter}, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadIgnoreSet cancelled mid-walk = %v, want context.Canceled", err)
	}
	if counter.calls > counter.cancelOn+2 {
		t.Fatalf("discovery kept walking after cancellation: %d ReadDir calls", counter.calls)
	}
}

// TestGlobWithBudgetRefusesANilBudget pins the entry-point half of the nil
// budget finding: a caller that omits the budget gets an error, not a panic
// via budget.full(), budget.listing() or budget.match() deeper in the walk.
func TestGlobWithBudgetRefusesANilBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithBudget(t.Context(), "**/needle.txt", root, true, nil)
	if err == nil {
		t.Fatalf("GlobWithBudget with a nil budget = (%v, %d, nil), want an error", matches, excluded)
	}
}

// TestSandboxedGlobReportsCancellation covers the arm the off-sandbox tests
// never reach: sandboxed sessions run the same walk over a secureDirFS, and a
// cancellation there must surface as an error too rather than as a
// plausible-looking empty result.
func TestSandboxedGlobReportsCancellation(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	nest := filepath.Join(worktree, "deep", "nest")
	if err := os.MkdirAll(nest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nest, "needle.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sanity: the same glob finds the file when nobody cancels it, so a
	// cancelled empty result cannot be mistaken for "there was nothing here".
	found, _, err := env.GlobWithExclusions(t.Context(), "**/needle.txt", worktree, true)
	if err != nil || len(found) != 1 {
		t.Fatalf("sandboxed glob = (%v, %v), want the one needle", found, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	matches, excluded, err := env.GlobWithExclusions(ctx, "**/needle.txt", worktree, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sandboxed GlobWithExclusions with a cancelled context = (%v, %d, %v), want context.Canceled", matches, excluded, err)
	}
	if matches != nil {
		t.Fatalf("sandboxed GlobWithExclusions after cancellation returned %v, want no matches", matches)
	}
}

// TestGlobMatchIsDirReportsCancellation covers the post-walk gitignore check:
// treating a cancelled stat as "not a directory" would silently disable
// directory-only .gitignore rules and hand back a plausible result with a nil
// error — the failure mode the walk itself no longer has.
func TestGlobMatchIsDirReportsCancellation(t *testing.T) {
	tree := fstest.MapFS{"dir/file.txt": &fstest.MapFile{Data: []byte("x")}}

	isDir, err := globMatchIsDir(t.Context(), cancelFS{ctx: t.Context(), fsys: tree}, "dir")
	if err != nil || !isDir {
		t.Fatalf("globMatchIsDir(dir) = (%v, %v), want (true, nil)", isDir, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	isDir, err = globMatchIsDir(ctx, cancelFS{ctx: ctx, fsys: tree}, "dir")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globMatchIsDir after cancellation = (%v, %v), want context.Canceled", isDir, err)
	}
	if isDir {
		t.Fatalf("globMatchIsDir after cancellation = true, want false")
	}
}

// TestSortPathsByMtimeDescStopsOnCancellation pins the last uncancellable
// stretch of the glob: stat'ing a large result set to order it. The check has
// to sit inside the stat loop, so cancelling part-way through stops the
// remaining stats rather than only being noticed once they are all done.
func TestSortPathsByMtimeDescStopsOnCancellation(t *testing.T) {
	paths := mtimeFixture(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stats := 0
	stubSortPathStat(t, func(string) {
		stats++
		if stats == 3 {
			cancel()
		}
	})

	err := sortPathsByMtimeDesc(ctx, paths)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sortPathsByMtimeDesc cancelled part-way = %v, want context.Canceled", err)
	}
	if stats > 3 {
		t.Fatalf("sort kept stat'ing after cancellation: %d of %d stats", stats, len(paths))
	}
}

// stubGlobBaseFS interposes on the fs.FS the off-sandbox glob walks, so a test
// can drive the exported entry point over a filesystem it controls, and
// restores the seam when the test ends.
func stubGlobBaseFS(t *testing.T, open func(ctx context.Context, dir string, budget *GlobBudget) fs.FS) {
	t.Helper()
	orig := globBaseFS
	globBaseFS = open
	t.Cleanup(func() { globBaseFS = orig })
}
