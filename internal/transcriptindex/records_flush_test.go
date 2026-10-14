package transcriptindex

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// countingFile wraps a record file and counts the bytes WriteAt puts on disk,
// so a test can pin how much of a buffered flush actually reaches the file.
type countingFile struct {
	*os.File
	written int64
}

func (c *countingFile) WriteAt(p []byte, off int64) (int, error) {
	c.written += int64(len(p))
	return c.File.WriteAt(p, off)
}

// openCountingTable opens a record file holding n records of the given size
// and buffers it through a counting wrapper, as openFiles does.
func openCountingTable(t *testing.T, size int64, n uint64) (*table, *countingFile) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "records"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.Write(make([]byte, int64(n)*size)); err != nil {
		t.Fatal(err)
	}
	w := &countingFile{File: f}
	tbl := &table{file: w, size: size, n: n}
	if err := tbl.load(); err != nil {
		t.Fatal(err)
	}
	return tbl, w
}

// appendAndReset commits one appended record and zeroes the write counter,
// so the flush a test triggers next shows exactly what that flush wrote.
func appendAndReset(t *testing.T, tbl *table, w *countingFile) {
	t.Helper()
	if _, err := tbl.append(make([]byte, tbl.size)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	w.written = 0
}

// A live writer commits one extension at a time: each flush must put only the
// newly appended records on disk, never the whole table again.
func TestAFlushWritesOnlyTheAppendedRecords(t *testing.T) {
	tbl, w := openCountingTable(t, itemRecordSize, 4)

	appendAndReset(t, tbl, w)

	if _, err := tbl.append(make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	if w.written != itemRecordSize {
		t.Fatalf("flush wrote %d bytes; only the %d-byte appended record is new",
			w.written, itemRecordSize)
	}
}

// An in-place write below the flushed watermark must reach the file too: the
// flush after it rewrites everything from that record onward.
func TestAnInPlaceRecordWriteMakesTheNextFlushRewriteFromIt(t *testing.T) {
	tbl, w := openCountingTable(t, itemRecordSize, 4)

	appendAndReset(t, tbl, w)

	if err := tbl.write(1, make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	if w.written != 4*itemRecordSize {
		t.Fatalf("flush wrote %d bytes after an in-place write at record 1; everything from record 1 on (%d bytes) must be rewritten",
			w.written, 4*itemRecordSize)
	}
}

// A live extension touches the record it keeps re-writing — the open turn —
// which sits at the table's end, so the flush after it must stay a cheap tail
// rather than a whole-table rewrite.
func TestAnInPlaceWriteAtTheTablesEndKeepsTheFlushCheap(t *testing.T) {
	tbl, w := openCountingTable(t, itemRecordSize, 4)

	appendAndReset(t, tbl, w)

	if err := tbl.write(4, make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	if w.written != itemRecordSize {
		t.Fatalf("flush wrote %d bytes after an in-place write at the last record; only that record (%d bytes) is stale",
			w.written, itemRecordSize)
	}
}

// In-place writes at two slots must make the flush rewrite from the lower
// one: the watermark is a floor an in-place write lowers to, never the last
// write's offset, or the earlier write's new bytes would strand on the
// file's old ones.
func TestAscendingInPlaceWritesMakeTheFlushRewriteFromTheLowest(t *testing.T) {
	tbl, w := openCountingTable(t, itemRecordSize, 4)

	appendAndReset(t, tbl, w)

	if err := tbl.write(1, make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.write(3, make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	if w.written != 4*itemRecordSize {
		t.Fatalf("flush wrote %d bytes after in-place writes at records 1 and 3; everything from record 1 on (%d bytes) must be rewritten",
			w.written, 4*itemRecordSize)
	}
}

// A table that buffered small content must stay honest after another handle
// grows the file past the buffer limit and this one adopts the growth: the
// buffered copy is stale and its length names nothing on disk, so reads go to
// the file and a flush never truncates the other handle's records away.
func TestAnAdoptedTableThatOutgrewTheBufferReadsTheFile(t *testing.T) {
	previous := recordBufferLimit
	recordBufferLimit = 4 * itemRecordSize
	t.Cleanup(func() { recordBufferLimit = previous })

	tbl, _ := openCountingTable(t, itemRecordSize, 2)
	if tbl.buf == nil {
		t.Fatal("the small table should be buffered")
	}

	// Another handle grows the file past the shrunk limit, and adoption
	// takes up its counts.
	if _, err := tbl.file.WriteAt(make([]byte, 8*itemRecordSize), 2*itemRecordSize); err != nil {
		t.Fatal(err)
	}
	tbl.n = 10

	// The reload an adoption performs sees a file past the limit.
	if err := tbl.load(); err != nil {
		t.Fatal(err)
	}

	record, err := tbl.read(9, 1)
	if err != nil {
		t.Fatalf("read past the old buffer: %v", err)
	}
	if len(record) != int(itemRecordSize) {
		t.Fatalf("record = %d bytes, want one whole record", len(record))
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	info, err := tbl.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 10*itemRecordSize {
		t.Fatalf("file holds %d bytes after the flush, want the other handle's %d kept",
			info.Size(), 10*itemRecordSize)
	}
}

// The strings file's buffer carries the same adoption hazard, and the same
// honesty: growth another handle committed past the limit survives this
// handle's reload and flush.
func TestAnAdoptedStringsFileThatOutgrewTheBufferKeepsTheFile(t *testing.T) {
	previous := recordBufferLimit
	recordBufferLimit = 16
	t.Cleanup(func() { recordBufferLimit = previous })

	f, err := os.Create(filepath.Join(t.TempDir(), "strings"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.WriteString("seed"); err != nil {
		t.Fatal(err)
	}
	b := &blob{file: f, n: 4}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	if b.buf == nil {
		t.Fatal("the small strings file should be buffered")
	}

	// Another handle grows the file past the shrunk limit; adoption takes
	// up its length.
	if _, err := f.WriteAt([]byte(" and much more than the buffer allows"), 4); err != nil {
		t.Fatal(err)
	}
	b.n = 41

	if err := b.load(); err != nil {
		t.Fatal(err)
	}

	if got, err := b.get(strRef{Off: 19, Len: 5}); err != nil {
		t.Fatalf("read past the old buffer: %v", err)
	} else if string(got) != "than " {
		t.Fatalf("string at 19 = %q, want the grown file's bytes", got)
	}
	if err := b.flush(); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 41 {
		t.Fatalf("file holds %d bytes after the flush, want the other handle's growth kept", info.Size())
	}
}

// TestACorruptStringReferenceCannotOverflowTheBoundsCheck pins the overflow
// the buffered path opened: a corrupt reference whose Off is MaxUint64 wraps
// an additive bounds check into passing, and the buffered slice then panics
// where the unbuffered ReadAt answered an error. The bounds check must reject
// by subtraction, which cannot wrap.
func TestACorruptStringReferenceCannotOverflowTheBoundsCheck(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "strings")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("seed"); err != nil {
		t.Fatal(err)
	}
	b := &blob{file: f, n: 4}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}

	if _, err := b.get(strRef{Off: math.MaxUint64, Len: 1}); !errors.Is(err, errCorrupt) {
		t.Fatalf("buffered get with an overflowing reference = %v, want the corrupt error, not a panic", err)
	}
	if _, err := b.get(strRef{Off: 4, Len: 1}); !errors.Is(err, errCorrupt) {
		t.Fatalf("buffered get at the file's end = %v, want the corrupt error", err)
	}

	// The unbuffered shape answers the same error, not a panic either.
	b.buf = nil
	b.flushed = 0
	if _, err := b.get(strRef{Off: math.MaxUint64, Len: 1}); !errors.Is(err, errCorrupt) {
		t.Fatalf("unbuffered get with an overflowing reference = %v, want the corrupt error", err)
	}
}

// TestACorruptSlotCannotOverflowTheReadBoundsCheck pins the table read's
// sibling of the string-reference overflow: a corrupt record slot of
// MaxUint64 wraps an additive slot+count check into passing, and the
// buffered slice then panics at a negative offset where the unbuffered
// ReadAt answered an error. A negative count must be rejected before make
// can see it.
func TestACorruptSlotCannotOverflowTheReadBoundsCheck(t *testing.T) {
	tbl, _ := openCountingTable(t, itemRecordSize, 4)

	if _, err := tbl.read(math.MaxUint64, 1); !errors.Is(err, errCorrupt) {
		t.Fatalf("read at an overflowing slot = %v, want the corrupt error, not a panic", err)
	}
	if _, err := tbl.read(0, -1); !errors.Is(err, errCorrupt) {
		t.Fatalf("read with a negative count = %v, want the corrupt error", err)
	}
	if _, err := tbl.read(4, 1); !errors.Is(err, errCorrupt) {
		t.Fatalf("read at the table's end = %v, want the corrupt error", err)
	}
}

// A buffered truncate only shrinks buf; the flush that publishes it must
// still shrink the file, or rows past the counts would survive a restart.
func TestATruncateShrinkReachesTheFileOnFlush(t *testing.T) {
	tbl, _ := openCountingTable(t, itemRecordSize, 4)

	if err := tbl.truncate(2); err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.append(make([]byte, itemRecordSize)); err != nil {
		t.Fatal(err)
	}
	if err := tbl.flush(); err != nil {
		t.Fatal(err)
	}
	info, err := tbl.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3*itemRecordSize {
		t.Fatalf("file holds %d bytes after the flush; the truncate to %d never reached it",
			info.Size(), 3*itemRecordSize)
	}
}

// The strings file grows the same way: a flush puts only the appended bytes.
func TestAStringsFlushWritesOnlyTheAppendedBytes(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "strings"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	w := &countingFile{File: f}
	b := &blob{file: w, n: 5}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}

	if _, err := b.put([]byte(" world")); err != nil {
		t.Fatal(err)
	}
	if err := b.flush(); err != nil {
		t.Fatal(err)
	}
	w.written = 0

	if _, err := b.put([]byte("!")); err != nil {
		t.Fatal(err)
	}
	if err := b.flush(); err != nil {
		t.Fatal(err)
	}
	if w.written != 1 {
		t.Fatalf("strings flush wrote %d bytes; only the appended byte is new", w.written)
	}
}
