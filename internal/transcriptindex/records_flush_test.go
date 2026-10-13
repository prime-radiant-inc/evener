package transcriptindex

import (
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
