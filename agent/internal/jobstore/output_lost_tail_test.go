package jobstore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// A power crash can drop the log's unsynced tail while the metadata rename that
// describes it reached the disk. Every reader must then report the bytes that
// survived instead of refusing the job (Jesse's 2026-11-16 ruling: lose the
// tail, never corrupt).
func TestOutputReadersSurviveALostUnsyncedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // the append after the barrier must not get its own barrier
	saved := []byte("line one\nline two\n")
	if _, err := store.Append(saved); err != nil {
		t.Fatalf("append saved: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Appended after the barrier, so its bytes are not durable when the machine
	// loses power.
	if _, err := store.Append([]byte("line three is lost\n")); err != nil {
		t.Fatalf("append lost: %v", err)
	}
	// The crash: the log keeps only the bytes the barrier covered.
	if err := os.Truncate(path, int64(len(saved))); err != nil {
		t.Fatalf("truncate to the durable tail: %v", err)
	}

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats after a lost tail: %v", err)
	}
	if total != int64(len(saved)) || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: the surviving bytes", total, visibleStart, len(saved))
	}

	reopened, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("reopen after a lost tail: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()
	tail, gotTotal, truncated, err := reopened.Tail(1 << 20)
	if err != nil {
		t.Fatalf("tail after a lost tail: %v", err)
	}
	if !bytes.Equal(tail, saved) || gotTotal != int64(len(saved)) || truncated {
		t.Fatalf("tail = %q/%d/%v, want the surviving %q", tail, gotTotal, truncated, saved)
	}
	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot after a lost tail: %v", err)
	}
	if !bytes.Equal(page.Content, saved) || page.TotalBytes != int64(len(saved)) {
		t.Fatalf("page = %q/%d, want the surviving %q", page.Content, page.TotalBytes, saved)
	}
}

// The same tolerance covers a tail cut mid-record, which is what a lost page
// cache leaves behind.
func TestOutputReadersSurviveALostTailCutMidRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // the append after the barrier must not get its own barrier
	saved := []byte("line one\n")
	if _, err := store.Append(saved); err != nil {
		t.Fatalf("append saved: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := store.Append([]byte("line two is torn\n")); err != nil {
		t.Fatalf("append torn: %v", err)
	}
	// Keep a torn prefix of the unsynced append.
	if err := os.Truncate(path, int64(len(saved)+4)); err != nil {
		t.Fatalf("truncate mid-record: %v", err)
	}
	total, _, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats after a torn tail: %v", err)
	}
	// The torn bytes past the barrier were never vouched for, so the job ends at
	// the last complete record rather than at the truncated end.
	if want := int64(len(saved)); total != want {
		t.Fatalf("OutputFileStats total = %d, want the last complete record %d", total, want)
	}
	reopened, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("reopen after a torn tail: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// A log that something replaced is not a lost tail, and must still be refused:
// the tolerance above is about bytes the crash could not save, not about a file
// whose content disagrees with every anchor.
func TestOutputStillRejectsALogThatContradictsItsAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Append([]byte("line one\nline two\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Replace the log with different content of the same length.
	if err := os.WriteFile(path, []byte("KORRUPT\nline two\n"), 0o644); err != nil {
		t.Fatalf("replace log: %v", err)
	}
	if _, _, err := OutputFileStats(path); err == nil {
		t.Fatal("OutputFileStats accepted a log that contradicts its anchor")
	}
}

// A store with barriers disabled publishes metadata that matches the log after
// every append, because it pays nothing to do so.
func TestOutputNoSyncStoreKeepsMetadataExactPerAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := CreateOutputNoSync(path, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()
	for _, chunk := range []string{"one\n", "two\n", "three\n"} {
		if _, err := store.Append([]byte(chunk)); err != nil {
			t.Fatalf("append: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		meta, ok, err := readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
		if err != nil || !ok {
			t.Fatalf("read metadata: ok=%v err=%v", ok, err)
		}
		if meta.TotalBytes != int64(len(raw)) || meta.RetainedSHA256 != outputBytesSHA256(raw) {
			t.Fatalf("metadata after %q describes %d bytes, want the log's %d", chunk, meta.TotalBytes, len(raw))
		}
	}
}

// The finish seam and Close both advance the durable anchor to describe the
// whole retained log, so a crash after a clean finish loses nothing.
func TestOutputFlushAndCloseAdvanceTheDurableAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.metaSyncInterval = time.Hour // hold the tick off; only Flush and Close act
	body := []byte(strings.Repeat("durable output\n", 4))
	if _, err := store.Append(body); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	meta, ok, err := readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
	if err != nil || !ok {
		t.Fatalf("read metadata: ok=%v err=%v", ok, err)
	}
	if meta.DurableFileSize == nil || *meta.DurableFileSize != int64(len(body)) {
		t.Fatalf("anchor after Flush = %v, want the whole %d-byte log", meta.DurableFileSize, len(body))
	}
	if meta.DurableSHA256 != outputBytesSHA256(body) {
		t.Fatal("anchor checksum after Flush does not describe the log")
	}
	if got := store.durableFileSize; got != int64(len(body)) {
		t.Fatalf("store anchor = %d, want %d", got, len(body))
	}
	// Close pays the barrier too, so an append after the Flush must be durable
	// once Close returns.
	more := []byte("after the flush\n")
	if _, err := store.Append(more); err != nil {
		t.Fatalf("append after flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	meta, ok, err = readOutputMeta(afero.NewOsFs(), outputMetaPath(path))
	if err != nil || !ok {
		t.Fatalf("read metadata after close: ok=%v err=%v", ok, err)
	}
	want := int64(len(body) + len(more))
	if meta.DurableFileSize == nil || *meta.DurableFileSize != want {
		t.Fatalf("anchor after Close = %v, want the whole %d-byte log", meta.DurableFileSize, want)
	}
	if meta.DurableSHA256 != outputBytesSHA256(append(append([]byte{}, body...), more...)) {
		t.Fatal("anchor checksum after Close does not describe the log")
	}
}
