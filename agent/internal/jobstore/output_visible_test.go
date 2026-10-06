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
