package execenv

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func stubMaxGlobDirListings(t *testing.T, n int) {
	t.Helper()
	orig := maxGlobDirListings
	maxGlobDirListings = n
	t.Cleanup(func() { maxGlobDirListings = orig })
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

// retained reports how many directory identities this walk is holding, which// retained reports how many directory identities this walk is holding, which
// is the walk's own memory cost: the cycle check consults only the ancestors
// of the directory it is admitting, so this must track the depth of the path
// being walked, not the number of directories the walk has ever listed.
func (w *globWalkFS) retained() int { return len(w.chain) }
