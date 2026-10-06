package jobstore

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

// tailRewriteCountingFS counts compactions: each one writes the retained
// tail to a sibling file and renames it over the output.
type tailRewriteCountingFS struct {
	afero.Fs
	output   string
	rewrites atomic.Int64
}

func (fs *tailRewriteCountingFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return fs.Fs.(afero.Lstater).LstatIfPossible(name)
}

func (fs *tailRewriteCountingFS) Rename(oldname, newname string) error {
	if newname == fs.output {
		fs.rewrites.Add(1)
	}
	return fs.Fs.Rename(oldname, newname)
}

// #3808: past the retention cap, every append rewrote and fsynced the whole
// retained tail. Compaction now waits until the file holds twice the cap, so a
// steady writer pays one rewrite per capBytes written, not one per append.
func TestOutputCompactsOncePerCapBytesWrittenPastTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_C.log")
	fs := &tailRewriteCountingFS{Fs: afero.NewOsFs(), output: path}
	const capBytes = 1000
	store, err := createOutputFsWithSync(fs, path, capBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunk := []byte(strings.Repeat("x", 99) + "\n")
	const appends = 100 // 10,000 bytes: 9,000 past the cap
	for range appends {
		if _, err := store.Append(chunk); err != nil {
			t.Fatal(err)
		}
	}
	written := int64(appends * len(chunk))
	maxRewrites := (written-capBytes)/capBytes + 1
	if got := fs.rewrites.Load(); got > maxRewrites {
		t.Fatalf("%d tail rewrites for %d bytes past a %d-byte cap, want at most %d (one per cap bytes written)", got, written-capBytes, capBytes, maxRewrites)
	}
}

// Whatever the file holds between compactions, every reader sees exactly the
// last capBytes of output, never anything older.
func TestOutputReadersSeeExactlyTheLastCapBytesBetweenCompactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_D.log")
	const capBytes = 64
	store, err := CreateOutputNoSync(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var all bytes.Buffer
	for i := range 40 {
		line := fmt.Sprintf("line-%02d\n", i)
		all.WriteString(line)
		if _, err := store.Append([]byte(line)); err != nil {
			t.Fatal(err)
		}
		total := int64(all.Len())
		first := max(total-capBytes, 0)
		want := all.Bytes()[first:]

		if got := store.RetainedStart(); got != first {
			t.Fatalf("after %d bytes: RetainedStart = %d, want %d", total, got, first)
		}
		tail, _, _, err := store.Tail(1 << 20)
		if err != nil || !bytes.Equal(tail, want) {
			t.Fatalf("after %d bytes: Tail = %q, %v; want %q", total, tail, err, want)
		}
		head, _, _, err := store.Head(1 << 20)
		if err != nil || !bytes.Equal(head, want) {
			t.Fatalf("after %d bytes: Head = %q, %v; want %q", total, head, err, want)
		}
		page, err := store.ReadPage(nil, 1<<20)
		if err != nil || page.Start != first || !bytes.Equal(page.Content, want) {
			t.Fatalf("after %d bytes: ReadPage = %+v, %v; want %q from %d", total, page, err, want, first)
		}
		matches, err := store.Grep(regexp.MustCompile("line"), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 || matches[0].ByteOffset < first {
			t.Fatalf("after %d bytes: Grep's first match at %+v, want none before %d", total, matches, first)
		}
		snapshot, err := ReadOutputPageSnapshot(path, nil, 1<<20)
		if err != nil || snapshot.Start != first || !bytes.Equal(snapshot.Content, want) {
			t.Fatalf("after %d bytes: path page = %+v, %v; want %q from %d", total, snapshot, err, want, first)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 2*capBytes {
		t.Fatalf("file holds %d bytes, want at most twice the %d-byte cap", info.Size(), capBytes)
	}
}
