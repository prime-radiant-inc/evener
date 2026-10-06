package jobstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// writeHiddenPrefixOutput leaves an output file whose first bytes sit before
// the visible start its metadata records: the shape a file has between
// compactions, while it still holds bytes older than the retention cap.
func writeHiddenPrefixOutput(t *testing.T) (path string, visibleStart int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "job_V.log")
	retained := []byte("old-a\nold-b\nnew-1\nnew-2\n")
	if err := os.WriteFile(path, retained, 0o644); err != nil {
		t.Fatal(err)
	}
	visibleStart = int64(len("old-a\nold-b\n")) + 100
	if err := writeOutputMetaFile(outputMetaPath(path), outputMeta{
		TotalBytes:          int64(len(retained)) + 100,
		RetainedStart:       100,
		RetainedSHA256:      outputBytesSHA256(retained),
		VisibleStart:        new(visibleStart),
		VisibleStartPartial: new(false),
	}); err != nil {
		t.Fatal(err)
	}
	return path, visibleStart
}

func TestOutputPathReadersNeverReturnBytesBeforeTheVisibleStart(t *testing.T) {
	path, visibleStart := writeHiddenPrefixOutput(t)
	want := []byte("new-1\nnew-2\n")

	for _, fromHead := range []bool{true, false} {
		snapshot, err := ReadOutputSnapshot(path, 1024, fromHead)
		if err != nil {
			t.Fatalf("snapshot fromHead=%v: %v", fromHead, err)
		}
		if !bytes.Equal(snapshot.Content, want) || snapshot.RetainedStart != visibleStart || snapshot.RetainedStartPartial || !snapshot.Truncated {
			t.Fatalf("snapshot fromHead=%v = %+v, want content %q from %d", fromHead, snapshot, want, visibleStart)
		}
	}

	page, err := ReadOutputPageSnapshot(path, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if page.Start != visibleStart || !bytes.Equal(page.Content, want) || page.RetainedStart != visibleStart {
		t.Fatalf("latest page = %+v, want %q from %d", page, want, visibleStart)
	}
	before := visibleStart - 1
	if _, err := ReadOutputPageSnapshot(path, &before, 1024); !errors.Is(err, ErrOutputPruned) {
		t.Fatalf("page before the visible start: err = %v, want ErrOutputPruned", err)
	}
	if _, err := ReadOutputWindowSnapshot(path, visibleStart-1, 1024); !errors.Is(err, ErrOutputPruned) {
		t.Fatalf("window before the visible start: err = %v, want ErrOutputPruned", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fdPage, err := ReadOutputPageSnapshotFromFile(path, f, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if fdPage.Start != visibleStart || !bytes.Equal(fdPage.Content, want) {
		t.Fatalf("descriptor page = %+v, want %q from %d", fdPage, want, visibleStart)
	}
	fdTail, err := ReadOutputSnapshotFromFile(path, f, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fdTail.Content, want) || fdTail.RetainedStart != visibleStart {
		t.Fatalf("descriptor tail = %+v, want %q from %d", fdTail, want, visibleStart)
	}
}

func TestOutputStoreOpenedOverAHiddenPrefixReadsOnlyTheVisibleBytes(t *testing.T) {
	path, visibleStart := writeHiddenPrefixOutput(t)
	// No cap, so nothing compacts or moves the visible start on open: the
	// store must honor the visible start its metadata records.
	o, err := OpenOutputNoSync(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	want := []byte("new-1\nnew-2\n")
	if got := o.RetainedStart(); got != visibleStart {
		t.Fatalf("RetainedStart = %d, want %d", got, visibleStart)
	}
	head, _, _, err := o.Head(1024)
	if err != nil || !bytes.Equal(head, want) {
		t.Fatalf("Head = %q, %v; want %q", head, err, want)
	}
	tail, _, _, err := o.Tail(1024)
	if err != nil || !bytes.Equal(tail, want) {
		t.Fatalf("Tail = %q, %v; want %q", tail, err, want)
	}
	page, err := o.ReadPage(nil, 1024)
	if err != nil || page.Start != visibleStart || !bytes.Equal(page.Content, want) {
		t.Fatalf("ReadPage = %+v, %v; want %q from %d", page, err, want, visibleStart)
	}
	matches, err := o.Grep(regexp.MustCompile("old|new"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ByteOffset != visibleStart {
		t.Fatalf("Grep = %+v, want only the two visible lines from %d", matches, visibleStart)
	}
}

// storeOverRaw opens an uncapped store over content, then sets the cap the
// way a store sees it once compaction is deferred: the file can hold more
// than capBytes, and refreshVisibleLocked has to hide the excess.
func storeOverRaw(t *testing.T, content string, capBytes int64) *OutputStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job_R.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := OpenOutputNoSync(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	o.capBytes = capBytes
	if err := o.refreshVisibleLocked(); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOutputVisibleStartSkipsACutRuneAndReadsTheLineBoundary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		capBytes    int64
		wantStart   int64
		wantPartial bool
	}{
		{"after a newline", "aaaa\nbbbb\n", 5, 5, false},
		{"mid line", "aaaa\nbbbb\n", 4, 6, true},
		{"inside a 2-byte rune", "x\né!\n", 3, 4, true},
		{"inside a 3-byte rune", "x\n€!\n", 3, 5, true},
		{"inside a 4-byte rune", "x\n😀!\n", 4, 6, true},
		{"a rune right after a newline", "x\n😀!\n", 6, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := storeOverRaw(t, tc.content, tc.capBytes)
			if o.visibleStart != tc.wantStart || o.visiblePartial != tc.wantPartial {
				t.Fatalf("visible = %d partial=%v, want %d partial=%v", o.visibleStart, o.visiblePartial, tc.wantStart, tc.wantPartial)
			}
			tail, _, _, err := o.Tail(1024)
			if err != nil || string(tail) != tc.content[tc.wantStart:] {
				t.Fatalf("Tail = %q, %v; want %q", tail, err, tc.content[tc.wantStart:])
			}
		})
	}
}

func TestOutputVisibleStartOnlyMovesForwardAndIsRecordedForReaders(t *testing.T) {
	// A cap of 5 cuts "three" after its "t", mid line.
	o := storeOverRaw(t, "one\ntwo\nthree\n", 5)
	if o.visibleStart != 9 || !o.visiblePartial {
		t.Fatalf("visible = %d partial=%v, want 9 partial", o.visibleStart, o.visiblePartial)
	}
	o.capBytes = 100
	if err := o.refreshVisibleLocked(); err != nil {
		t.Fatal(err)
	}
	if o.visibleStart != 9 {
		t.Fatalf("a larger cap moved the visible start back to %d", o.visibleStart)
	}
	if err := o.persistMetaLocked(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadOutputPageSnapshot(o.path, nil, 1024)
	if err != nil || page.RetainedStart != 9 || !page.RetainedStartPartial || string(page.Content) != "hree\n" {
		t.Fatalf("path page = %+v, %v; want \"hree\\n\" from 9, partial", page, err)
	}
	reopened, err := OpenOutputNoSync(o.path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.RetainedStart() != 9 || !reopened.RetainedStartPartial() {
		t.Fatalf("reopened visible = %d partial=%v, want 9 partial", reopened.RetainedStart(), reopened.RetainedStartPartial())
	}
}

func TestOutputStoreWindowAndForwardReadsStopAtTheVisibleStart(t *testing.T) {
	o := storeOverRaw(t, "old\n€uro\n", 6)
	// The cap cuts inside "€", so the visible start skips to "uro".
	if o.visibleStart != 7 {
		t.Fatalf("visible = %d, want 7", o.visibleStart)
	}
	buf, start, _, _, err := o.Window(0, 1024)
	if err != nil || start != 7 || string(buf) != "uro\n" {
		t.Fatalf("Window = %q from %d, %v; want \"uro\\n\" from 7", buf, start, err)
	}
	if _, err := o.ReadWindow(6, 10); !errors.Is(err, ErrOutputPruned) {
		t.Fatalf("ReadWindow below the visible start: err = %v, want ErrOutputPruned", err)
	}
	forward, err := o.ReadWindow(7, 10)
	if err != nil || string(forward.Content) != "uro\n" || !forward.Truncated {
		t.Fatalf("ReadWindow = %+v, %v; want \"uro\\n\", truncated", forward, err)
	}
}

func TestOutputFileStatsAndGrepSeeOnlyTheVisibleBytes(t *testing.T) {
	path, visibleStart := writeHiddenPrefixOutput(t)
	total, firstVisible, err := OutputFileStats(path)
	if err != nil || total != visibleStart+int64(len("new-1\nnew-2\n")) || firstVisible != visibleStart {
		t.Fatalf("OutputFileStats = %d, %d, %v; want first visible %d", total, firstVisible, err, visibleStart)
	}
	matches, grepTotal, err := GrepOutputFileLimit(path, regexp.MustCompile("old|new"), 1024, 0, 1024)
	if err != nil || grepTotal != total {
		t.Fatalf("GrepOutputFileLimit total = %d, %v; want %d", grepTotal, err, total)
	}
	if len(matches) != 2 || matches[0].ByteOffset != visibleStart || matches[0].Line != "new-1" {
		t.Fatalf("GrepOutputFileLimit = %+v, want the two visible lines from %d", matches, visibleStart)
	}
}
