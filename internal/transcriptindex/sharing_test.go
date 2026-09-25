package transcriptindex

import (
	"os"
	"sync"
	"testing"
)

func TestCatchUpToStopsAtTheLastCompleteLineWithinTheLength(t *testing.T) {
	fx := everything()
	path, lines := writeHeaderOnly(t, fx)
	x := openIndex(t, path, t.TempDir())
	header, _ := fx.encode(t)
	cut := len(lines) / 2
	appendBytes(t, path, joinLines(lines))
	covered := int64(len(header) + len(joinLines(lines[:cut])))

	// A length inside the next line covers only the lines before it.
	if err := x.CatchUpTo(covered + int64(len(lines[cut]))/2); err != nil {
		t.Fatal(err)
	}
	prefix := writeFixture(t, fixture{header: fx.header, lines: fx.lines[:cut]})
	want := referenceCandidates(t, prefix)
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if window.Length != covered {
		t.Fatalf("window length = %d, want %d", window.Length, covered)
	}
	assertWindow(t, "prefix", window, want, len(want), 40)

	catchUp(t, x)
	assertAllWindows(t, x, path)
}

func TestIncarnationIsStableAcrossAppendsAndChangesOnRebuild(t *testing.T) {
	fx := everything()
	// The corpus's nameless results make this handle rebuild for itself as it
	// extends; that keeps the incarnation, because the file still extends.
	path, lines := writeHeaderOnly(t, fx)
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, joinLines(lines[:10]))
	catchUp(t, x)
	first, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, joinLines(lines[10:]))
	catchUp(t, x)
	grown, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if first.Incarnation == "" || grown.Incarnation != first.Incarnation {
		t.Fatalf("appends changed the incarnation: %q then %q", first.Incarnation, grown.Incarnation)
	}
	if err := os.Truncate(path, first.Length); err != nil {
		t.Fatal(err)
	}
	catchUp(t, x)
	truncated, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if truncated.Incarnation == first.Incarnation {
		t.Fatal("a truncated transcript kept the incarnation")
	}
}

// Two handles on one sidecar stand for the hub and a daemon: separate opens of
// the lock file, as two processes would have.
func TestTwoHandlesShareOneSidecar(t *testing.T) {
	fx := namedResults()
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	a := openIndex(t, path, dir)
	b := openIndex(t, path, dir)
	cut := len(lines) / 2
	appendBytes(t, path, joinLines(lines[:cut]))
	catchUp(t, a)
	// b reads what a covered without catching up itself.
	want := referenceCandidates(t, path)
	window, err := b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "b after a extended", window, want, len(want), 40)

	// b extends past a, from records a wrote.
	appendBytes(t, path, joinLines(lines[cut:]))
	catchUp(t, b)
	assertAllWindows(t, b, path)
	assertAllWindows(t, a, path)
	if a.rebuilds+b.rebuilds != 1 {
		t.Fatalf("builds = %d, want only the first", a.rebuilds+b.rebuilds)
	}

	// a rebuilds; b holds the old incarnation and reopens on its next read.
	if err := os.Truncate(path, window.Length); err != nil {
		t.Fatal(err)
	}
	catchUp(t, a)
	after, err := b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := a.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if after.Incarnation != rebuilt.Incarnation {
		t.Fatalf("b reads incarnation %q after a rebuilt %q", after.Incarnation, rebuilt.Incarnation)
	}
	assertAllWindows(t, b, path)
}

func TestConcurrentExtendersWhileAppending(t *testing.T) {
	fx := namedResults()
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	handles := []*Index{openIndex(t, path, dir), openIndex(t, path, dir), openIndex(t, path, dir)}
	var wg sync.WaitGroup
	done := make(chan struct{})
	for _, x := range handles {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				if err := x.CatchUp(); err != nil {
					t.Error(err)
					return
				}
				if _, err := x.Latest(40); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for _, line := range lines {
		appendBytes(t, path, line)
	}
	close(done)
	wg.Wait()
	for _, x := range handles {
		catchUp(t, x)
		assertAllWindows(t, x, path)
	}
}
