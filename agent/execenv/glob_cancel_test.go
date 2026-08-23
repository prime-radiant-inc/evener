package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

// cancelAfterNthReadDir cancels the walk's context part-way through the
// traversal, on the nth ReadDir, so the glob is interrupted mid-walk rather
// than before it starts.
type cancelAfterNthReadDir struct {
	fs.FS
	n      int
	calls  int
	cancel context.CancelFunc
}

func (c *cancelAfterNthReadDir) ReadDir(name string) ([]fs.DirEntry, error) {
	c.calls++
	if c.calls == c.n {
		c.cancel()
	}
	return fs.ReadDir(c.FS, name)
}

// TestGlobMatchesStopsOnContextCancellation proves the other half of #369: an
// in-flight glob must abort when its context is cancelled and report the
// cancellation, so job_stop and session teardown can actually stop a walk
// instead of only asking. doublestar swallows I/O errors, so a cancelled walk
// would otherwise come back as a short result with a nil error.
func TestGlobMatchesStopsOnContextCancellation(t *testing.T) {
	tree := fstest.MapFS{}
	for _, dir := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		for _, sub := range []string{"one", "two", "three"} {
			tree[filepath.Join(dir, sub, "needle.txt")] = &fstest.MapFile{Data: []byte("x")}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counter := &cancelAfterNthReadDir{FS: tree, n: 3, cancel: cancel}
	fsys := cancelFS{ctx: ctx, fsys: counter}

	matches, err := globMatches(ctx, fsys, "**/needle.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globMatches after mid-walk cancellation = (%v, %v), want context.Canceled", matches, err)
	}
	if matches != nil {
		t.Fatalf("globMatches after cancellation returned partial matches %v, want none", matches)
	}
	// The walk must stop at the cancellation, not run the tree out: 8 dirs
	// with 3 subdirs each is 30+ ReadDir calls when it completes.
	if counter.calls > 6 {
		t.Fatalf("walk kept reading after cancellation: %d ReadDir calls", counter.calls)
	}
}

// TestGlobWithExclusionsReportsCancellation checks the same contract through
// the exported entry point the find_files tool calls, so the cancellation is
// surfaced to the tool rather than being flattened into an empty result.
func TestGlobWithExclusionsReportsCancellation(t *testing.T) {
	root, _ := globLoopFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	matches, excluded, err := NewLocalExecutionEnvironment(root).GlobWithExclusions(ctx, "**/needle.txt", root, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GlobWithExclusions with a cancelled context = (%v, %d, %v), want context.Canceled", matches, excluded, err)
	}
}
