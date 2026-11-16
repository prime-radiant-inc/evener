package jobstore

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/spf13/afero"
)

// These tests pin the one rule that covers every crash shape in which the log
// holds bytes the sidecar cannot vouch for: report the longest prefix the
// sidecar DOES vouch for, and never let a reader see past it. A power crash may
// lose the tail (Jesse's 2026-11-16 ruling); it may not turn the tail into
// output, and it may not abort session restore.

// tornSuffixFile writes a log of logBytes and the sidecar that describes it.
func tornSuffixFile(t *testing.T, logBytes []byte, meta outputMeta) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, logBytes, 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := writeOutputMetaFileFs(afero.NewOsFs(), outputMetaPath(path), meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	return path
}

// The sidecar names more bytes than the log holds, the anchor vouches for a
// 10-byte prefix, and the log still carries 8 torn bytes past it. Recovery
// reports the anchored prefix, and no reader may hand out the torn suffix.
func TestOutputTornSuffixPastAnchorIsNeverReported(t *testing.T) {
	logBytes := []byte("0123456789tornbyte")
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      40,
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256(bytes.Repeat([]byte("x"), 40)),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(logBytes[:10]),
	})

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats with a torn suffix: %v", err)
	}
	if total != 10 || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want 10/0: only the anchored prefix", total, visibleStart)
	}

	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot with a torn suffix: %v", err)
	}
	if !bytes.Equal(page.Content, logBytes[:10]) {
		t.Fatalf("page content = %q, want the anchored prefix %q: the torn suffix is not output",
			page.Content, logBytes[:10])
	}
}

// A readable failure is not the only trap: the paging reader must agree with
// the head/tail readers instead of refusing the file.
func TestOutputTornSuffixLeavesReadersAgreeing(t *testing.T) {
	logBytes := []byte("0123456789tornbyte")
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      40,
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256(bytes.Repeat([]byte("x"), 40)),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(logBytes[:10]),
	})

	snapshot, err := readOutputSnapshotAtPath(path, 1<<20, false)
	if err != nil {
		t.Fatalf("tail snapshot with a torn suffix: %v", err)
	}
	if !bytes.Equal(snapshot.Content, logBytes[:10]) || snapshot.TotalBytes != 10 {
		t.Fatalf("tail = %q/%d, want the anchored prefix %q/10", snapshot.Content, snapshot.TotalBytes, logBytes[:10])
	}
}

// The same length can hide a torn suffix when the crash preserved the file's
// size but not its bytes. The anchor still matches the prefix, so recovery
// reports that prefix instead of refusing the job (which aborts restore).
func TestOutputEqualLengthTornSuffixFallsBackToTheAnchor(t *testing.T) {
	// Ten vouched bytes followed by ten torn ones, so the log is exactly the 20
	// bytes the sidecar names: the crash kept the file's size, not its contents.
	logBytes := []byte("0123456789tornbytesx")
	if len(logBytes) != 20 {
		t.Fatalf("log is %d bytes, want the sidecar's 20", len(logBytes))
	}
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      20,
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256([]byte("0123456789otherbyte")),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(logBytes[:10]),
	})

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats with an equal-length torn suffix: %v", err)
	}
	if total != 10 || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want 10/0: the anchored prefix", total, visibleStart)
	}
	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot with an equal-length torn suffix: %v", err)
	}
	if !bytes.Equal(page.Content, logBytes[:10]) {
		t.Fatalf("page content = %q, want the anchored prefix %q", page.Content, logBytes[:10])
	}
}

// A writable reopen owns the file, so it must not append past a suffix nothing
// vouched for: it truncates to the anchored prefix and continues from there.
func TestOutputWritableReopenDropsTheTornSuffix(t *testing.T) {
	logBytes := []byte("0123456789tornbyte")
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      40,
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256(bytes.Repeat([]byte("x"), 40)),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(logBytes[:10]),
	})

	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("writable reopen with a torn suffix: %v", err)
	}
	if got := store.Len(); got != 10 {
		t.Fatalf("reopened Len = %d, want 10: the anchored prefix", got)
	}
	if _, err := store.Append([]byte("next\n")); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() != 15 {
		t.Fatalf("log size = %d, want 15: the anchored prefix plus the append, with no torn bytes kept", info.Size())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Equal(got, []byte("0123456789next\n")) {
		t.Fatalf("log = %q, want the anchored prefix plus the append", got)
	}
}

// Grep must agree with the snapshots over a recovered shorter file. Fencing on
// the verified window instead of the observed physical size made grep refuse a
// file every other reader accepted, and reopening the reader does not fix it
// because reconciliation never truncates the log.
func TestOutputGrepAgreesWithSnapshotsOverATornSuffix(t *testing.T) {
	logBytes := []byte("0123456789tornbyte")
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      40,
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256(bytes.Repeat([]byte("x"), 40)),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(logBytes[:10]),
	})

	matches, err := grepOpenedOutputFile(t, path, regexp.MustCompile("0123"), 1<<20, 0, 1024, func(int64, int64) error { return nil })
	if err != nil {
		t.Fatalf("grep refused a file the snapshots accept: %v", err)
	}
	if len(matches) != 1 || matches[0].ByteOffset != 0 {
		t.Fatalf("grep = %+v, want the anchored prefix match at 0", matches)
	}
	// The suffix must not be searched either: it is not output.
	none, err := grepOpenedOutputFile(t, path, regexp.MustCompile("torn"), 1<<20, 0, 1024, func(int64, int64) error { return nil })
	if err != nil {
		t.Fatalf("grep over the torn suffix: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("grep matched %+v inside the unverified suffix, want no match", none)
	}
}

// An intact anchored read verifies the window once. Hashing the whole file a
// second time doubles the cost of every ordinary read of a finished job.
func TestOutputIntactReadHashesTheFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	line := []byte("line one\n")
	if _, err := store.Append(line); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var opens int
	counter := &countingOutputOpenFS{Fs: afero.NewOsFs(), target: path, opens: &opens}
	meta, ok, err := readValidOutputMetaFs(counter, outputMetaPath(path), path, int64(len(line)))
	if err != nil || !ok {
		t.Fatalf("intact read: ok=%v err=%v", ok, err)
	}
	if meta.TotalBytes != int64(len(line)) {
		t.Fatalf("total = %d, want %d", meta.TotalBytes, len(line))
	}
	if opens != 1 {
		t.Fatalf("an intact read opened the output %d times, want 1: the verified window is the whole file", opens)
	}
}

// countingOutputOpenFS counts opens of one path, so a test can tell a single
// verification pass from a redundant second hash.
type countingOutputOpenFS struct {
	afero.Fs
	target string
	opens  *int
}

func (c *countingOutputOpenFS) Open(name string) (afero.File, error) {
	if name == c.target {
		*c.opens++
	}
	return c.Fs.Open(name)
}

// A sidecar older than the log is what an append outside the store leaves
// behind, and such an append writes whole records: the pin above recovers
// "more\n". A power crash leaves the opposite shape, a partial trailing record.
// That difference is a discriminator, so recovery can adopt a complete-record
// suffix and must refuse a torn one instead of hashing it and calling it
// validated output.
func TestOutputOlderSidecarDoesNotAdoptATornSuffix(t *testing.T) {
	vouched := []byte("keep\n")
	logBytes := append(append([]byte{}, vouched...), []byte("keepmore")...)
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      int64(len(vouched)),
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256(vouched),
		DurableFileSize: new(int64(len(vouched))),
		DurableSHA256:   outputBytesSHA256(vouched),
	})

	total, retainedStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats with an older sidecar and a torn suffix: %v", err)
	}
	if total != int64(len(vouched)) || retainedStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: the vouched prefix, not the torn suffix",
			total, retainedStart, len(vouched))
	}

	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot: %v", err)
	}
	if !bytes.Equal(page.Content, vouched) {
		t.Fatalf("page content = %q, want %q: the torn suffix is not output", page.Content, vouched)
	}
}

// A sidecar window can be damaged between the durable anchor and the end the
// sidecar names. The anchor still vouches for the prefix that survived, so
// recovery reports it instead of refusing: refusing fails OutputFileStats and
// aborts session restore over output that is recoverable.
func TestOutputAnchorRecoversWhenTheRecordedWindowIsDamaged(t *testing.T) {
	vouched := []byte("keep\n")
	// The log is longer than the window the sidecar names, so this takes the
	// longer-log arm. The sidecar's own checksum for that window does not match,
	// which is the case that used to refuse before trying the anchor.
	logBytes := []byte("keep\nXXXXXXXXXXXX")
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:      int64(len(vouched)),
		RetainedStart:   0,
		RetainedSHA256:  outputBytesSHA256([]byte("not these bytes")),
		DurableFileSize: new(int64(len(vouched))),
		DurableSHA256:   outputBytesSHA256(vouched),
	})

	total, retainedStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats with a damaged recorded window: %v", err)
	}
	if total != int64(len(vouched)) || retainedStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: the anchored prefix", total, retainedStart, len(vouched))
	}
	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot: %v", err)
	}
	if !bytes.Equal(page.Content, vouched) {
		t.Fatalf("page content = %q, want %q", page.Content, vouched)
	}
}
