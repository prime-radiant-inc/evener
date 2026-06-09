package jobstore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func appendOutput(t *testing.T, o *OutputStore, s string) {
	t.Helper()
	n, err := o.Append([]byte(s))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if n != len(s) {
		t.Fatalf("append wrote %d bytes, want %d", n, len(s))
	}
}

func appendFile(path string, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()
	if n, err := f.WriteString(s); err != nil {
		return err
	} else if n != len(s) {
		return io.ErrShortWrite
	}
	return nil
}

func TestOutputAppendAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1024)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "line1\n")
	appendOutput(t, o, "line2\n")

	data, total, truncated, err := o.Tail(1024)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if string(data) != "line1\nline2\n" {
		t.Errorf("tail = %q", data)
	}
	if total != 12 || truncated {
		t.Errorf("total=%d truncated=%v, want 12/false", total, truncated)
	}
}

func TestOutputTailTruncatesToLastBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "aaaaaXXXXX") // 10 bytes
	data, total, truncated, err := o.Tail(4)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if string(data) != "XXXX" {
		t.Errorf("tail(4) = %q, want last 4 bytes", data)
	}
	if total != 10 || !truncated {
		t.Errorf("total=%d truncated=%v, want 10/true", total, truncated)
	}
}

func TestOutputEnforcesCapAndReportsLifetimeBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 6)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "abcdef")
	appendOutput(t, o, "ghij")

	data, total, truncated, err := o.Tail(1024)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if string(data) != "efghij" {
		t.Fatalf("tail = %q, want capped retained tail", data)
	}
	if total != 10 || !truncated {
		t.Fatalf("total=%d truncated=%v, want lifetime 10 and truncated", total, truncated)
	}
}

func TestOutputCapGrepScansRetainedTailOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, int64(len("keep ready\n")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "drop ready\n")
	appendOutput(t, o, "keep ready\n")

	matches, err := o.Grep(regexp.MustCompile(`ready`), 1<<16)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "keep ready" {
		t.Fatalf("matches = %+v, want retained match only", matches)
	}
	if matches[0].ByteOffset != int64(len("drop ready\n")) {
		t.Fatalf("byte offset = %d, want lifetime offset", matches[0].ByteOffset)
	}
}

func TestOutputPrunedLifetimeSurvivesReopenAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "drop\n")
	appendOutput(t, o, "keep\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenOutput(path, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	data, total, truncated, err := reopened.Tail(1024)
	if err != nil {
		t.Fatalf("tail after reopen: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("tail after reopen = %q, want retained tail", data)
	}
	if total != int64(len("drop\nkeep\n")) || !truncated {
		t.Fatalf("after reopen total=%d truncated=%v, want lifetime 10 and truncated", total, truncated)
	}
	matches, err := reopened.Grep(regexp.MustCompile(`keep`), 1024)
	if err != nil {
		t.Fatalf("grep after reopen: %v", err)
	}
	if len(matches) != 1 || matches[0].ByteOffset != int64(len("drop\n")) {
		t.Fatalf("matches after reopen = %+v, want keep at lifetime offset 5", matches)
	}

	appendOutput(t, reopened, "more\n")
	data, total, truncated, err = reopened.Tail(1024)
	if err != nil {
		t.Fatalf("tail after append: %v", err)
	}
	if string(data) != "more\n" {
		t.Fatalf("tail after append = %q, want newly retained tail", data)
	}
	if total != int64(len("drop\nkeep\nmore\n")) || !truncated {
		t.Fatalf("after append total=%d truncated=%v, want lifetime 15 and truncated", total, truncated)
	}
	matches, err = reopened.Grep(regexp.MustCompile(`more`), 1024)
	if err != nil {
		t.Fatalf("grep after append: %v", err)
	}
	if len(matches) != 1 || matches[0].ByteOffset != int64(len("drop\nkeep\n")) {
		t.Fatalf("matches after append = %+v, want more at lifetime offset 10", matches)
	}
}

func TestOutputRejectsStaleMetadataForPrunedRetainedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "drop\n")
	appendOutput(t, o, "keep\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := os.WriteFile(path, []byte("more\n"), 0o644); err != nil {
		t.Fatalf("replace retained output with stale metadata: %v", err)
	}
	if _, err := OpenOutput(path, int64(len("keep\n"))); err == nil {
		t.Fatal("reopen with stale sidecar succeeded, want metadata mismatch")
	}
	if _, _, err := OutputFileStats(path); err == nil {
		t.Fatal("OutputFileStats with stale sidecar succeeded, want metadata mismatch")
	}
}

func TestOutputRecoversStaleMetadataAfterInterruptedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "drop\n")
	appendOutput(t, o, "keep\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := appendFile(path, "more\n"); err != nil {
		t.Fatalf("append output outside store: %v", err)
	}

	total, retainedStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("stats after stale sidecar append: %v", err)
	}
	if total != int64(len("drop\nkeep\nmore\n")) || retainedStart != int64(len("drop\n")) {
		t.Fatalf("stats total=%d retainedStart=%d, want recovered appended output", total, retainedStart)
	}

	reopened, err := OpenOutput(path, int64(len("keep\nmore\n")))
	if err != nil {
		t.Fatalf("reopen after stale sidecar append: %v", err)
	}
	data, total, truncated, err := reopened.Tail(1024)
	if err != nil {
		t.Fatalf("tail after recovery: %v", err)
	}
	if string(data) != "keep\nmore\n" {
		t.Fatalf("tail after recovery = %q, want retained output plus appended bytes", data)
	}
	if total != int64(len("drop\nkeep\nmore\n")) || !truncated {
		t.Fatalf("tail total=%d truncated=%v, want recovered lifetime and truncated", total, truncated)
	}
}

func TestOutputRejectsCorruptPrefixWithStaleMetadataAfterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "drop\n")
	appendOutput(t, o, "keep\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.WriteFile(path, []byte("KORRUPT\nmore\n"), 0o644); err != nil {
		t.Fatalf("replace output with corrupt prefix: %v", err)
	}

	if _, _, err := OutputFileStats(path); err == nil {
		t.Fatal("OutputFileStats with corrupt retained prefix succeeded, want metadata mismatch")
	}
	if _, err := OpenOutput(path, int64(len("keep\nmore\n"))); err == nil {
		t.Fatal("reopen with corrupt retained prefix succeeded, want metadata mismatch")
	}
}

func TestOutputRecoversPendingMetadataWhenFinalSidecarIsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	retained := []byte("same\n")
	if err := os.WriteFile(path, retained, 0o644); err != nil {
		t.Fatalf("write retained output: %v", err)
	}
	if err := writeOutputMetaFile(outputMetaPath(path), outputMeta{
		TotalBytes:     int64(len("drop\nsame\n")),
		RetainedStart:  int64(len("drop\n")),
		RetainedSHA256: outputBytesSHA256(retained),
	}); err != nil {
		t.Fatalf("write stale final metadata: %v", err)
	}
	if err := writeOutputMetaFile(outputPendingMetaPath(outputMetaPath(path)), outputMeta{
		TotalBytes:     int64(len("drop\nsame\nsame\n")),
		RetainedStart:  int64(len("drop\nsame\n")),
		RetainedSHA256: outputBytesSHA256(retained),
	}); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}

	o, err := OpenOutput(path, int64(len(retained)))
	if err != nil {
		t.Fatalf("reopen with pending metadata: %v", err)
	}
	data, total, truncated, err := o.Tail(1024)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !bytes.Equal(data, retained) {
		t.Fatalf("tail = %q, want retained output", data)
	}
	if total != int64(len("drop\nsame\nsame\n")) || !truncated {
		t.Fatalf("total=%d truncated=%v, want recovered lifetime and truncated", total, truncated)
	}
	matches, err := o.Grep(regexp.MustCompile(`same`), 1024)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].ByteOffset != int64(len("drop\nsame\n")) {
		t.Fatalf("matches = %+v, want recovered lifetime offset", matches)
	}
	if _, err := os.Stat(outputPendingMetaPath(outputMetaPath(path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending metadata stat err = %v, want removed", err)
	}
}

func TestOutputRecoversPendingMetadataBeforeDestructivePrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	retained := []byte("new\n")
	if err := os.WriteFile(path, []byte("old\nnew\n"), 0o644); err != nil {
		t.Fatalf("write unpruned output: %v", err)
	}
	if err := writeOutputMetaFile(outputMetaPath(path), outputMeta{
		TotalBytes:     int64(len("old\n")),
		RetainedStart:  0,
		RetainedSHA256: outputBytesSHA256([]byte("old\n")),
	}); err != nil {
		t.Fatalf("write stale final metadata: %v", err)
	}
	if err := writeOutputMetaFile(outputPendingMetaPath(outputMetaPath(path)), outputMeta{
		TotalBytes:     int64(len("old\nnew\n")),
		RetainedStart:  int64(len("old\n")),
		RetainedSHA256: outputBytesSHA256(retained),
	}); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}

	total, retainedStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("stats with pending metadata before prune: %v", err)
	}
	if total != int64(len("old\nnew\n")) || retainedStart != 0 {
		t.Fatalf("stats total=%d retainedStart=%d, want full current retained output", total, retainedStart)
	}

	o, err := OpenOutput(path, int64(len(retained)))
	if err != nil {
		t.Fatalf("reopen with pending metadata before prune: %v", err)
	}
	data, total, truncated, err := o.Tail(1024)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !bytes.Equal(data, retained) {
		t.Fatalf("tail = %q, want retained output", data)
	}
	if total != int64(len("old\nnew\n")) || !truncated {
		t.Fatalf("total=%d truncated=%v, want pending lifetime and truncated", total, truncated)
	}
}

func TestOutputRejectsCorruptPendingMetadataWithoutFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	retained := []byte("same\n")
	if err := os.WriteFile(path, retained, 0o644); err != nil {
		t.Fatalf("write retained output: %v", err)
	}
	if err := writeOutputMetaFile(outputMetaPath(path), outputMeta{
		TotalBytes:     int64(len("drop\nsame\n")),
		RetainedStart:  int64(len("drop\n")),
		RetainedSHA256: outputBytesSHA256(retained),
	}); err != nil {
		t.Fatalf("write final metadata: %v", err)
	}
	if err := os.WriteFile(outputPendingMetaPath(outputMetaPath(path)), []byte("{not json}\n"), 0o644); err != nil {
		t.Fatalf("write corrupt pending metadata: %v", err)
	}

	if _, err := OpenOutput(path, int64(len(retained))); err == nil {
		t.Fatal("reopen with corrupt pending metadata succeeded, want error")
	}
	if _, _, err := OutputFileStats(path); err == nil {
		t.Fatal("OutputFileStats with corrupt pending metadata succeeded, want error")
	}
}

func TestWriteOutputMetaFileReplacesReadableMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	retained := []byte("tail\n")
	if err := os.WriteFile(path, retained, 0o644); err != nil {
		t.Fatalf("write retained output: %v", err)
	}
	metaPath := outputMetaPath(path)
	want := outputMeta{
		TotalBytes:     int64(len("drop\n") + len(retained)),
		RetainedStart:  int64(len("drop\n")),
		RetainedSHA256: outputBytesSHA256(retained),
	}
	if err := writeOutputMetaFile(metaPath, want); err != nil {
		t.Fatalf("write output metadata: %v", err)
	}
	if _, err := os.Stat(metaPath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary metadata stat err = %v, want removed", err)
	}
	got, ok, err := readValidOutputMeta(metaPath, path, int64(len(retained)))
	if err != nil {
		t.Fatalf("read output metadata: %v", err)
	}
	if !ok {
		t.Fatal("read output metadata ok=false, want true")
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
}

func TestOutputTailRejectsNegativeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "abc")

	_, _, _, err = o.Tail(-1)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("tail(-1) err = %v, want ErrInvalidLimit", err)
	}
}

func TestOutputTailZeroLimitReturnsOnlyMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	data, total, truncated, err := o.Tail(0)
	if err != nil {
		t.Fatalf("tail empty: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("tail(0) empty = %q, want empty data", data)
	}
	if total != 0 || truncated {
		t.Errorf("empty total=%d truncated=%v, want 0/false", total, truncated)
	}

	appendOutput(t, o, "abc")

	data, total, truncated, err = o.Tail(0)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("tail(0) = %q, want empty data", data)
	}
	if total != 3 || !truncated {
		t.Errorf("total=%d truncated=%v, want 3/true", total, truncated)
	}
}

func TestOutputGrepReturnsMatchesWithOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "starting\nserver ready\ndone\n")
	re := regexp.MustCompile(`(?i)ready`)
	matches, err := o.Grep(re, 1<<16)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "server ready" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].ByteOffset != int64(len("starting\n")) {
		t.Errorf("byte offset = %d, want %d", matches[0].ByteOffset, len("starting\n"))
	}
}

func TestOutputGrepReturnsCRLFByteOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "a\r\nready\r\n")
	re := regexp.MustCompile(`ready`)

	matches, err := o.Grep(re, 1<<16)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "ready" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].ByteOffset != 3 {
		t.Errorf("byte offset = %d, want 3", matches[0].ByteOffset)
	}
}

func TestOutputGrepLimitValidationAndBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "prefix\nready-one\nready-two\n")
	re := regexp.MustCompile(`ready`)

	matches, err := o.Grep(re, -1)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("grep(-1) err = %v, want ErrInvalidLimit", err)
	}
	if len(matches) != 0 {
		t.Fatalf("grep(-1) matches = %+v, want none", matches)
	}

	matches, err = o.Grep(re, 0)
	if err != nil {
		t.Fatalf("grep(0): %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("grep(0) matches = %+v, want none", matches)
	}

	matches, err = o.Grep(re, len("ready-one")-1)
	if err != nil {
		t.Fatalf("grep small: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("grep small matches = %+v, want none", matches)
	}

	matches, err = o.Grep(re, len("ready-one")+len("ready-two")-1)
	if err != nil {
		t.Fatalf("grep budget: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "ready-one" {
		t.Fatalf("grep budget matches = %+v, want ready-one only", matches)
	}
}

func TestOutputGrepLimitCapsZeroLengthMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 100; i++ {
		appendOutput(t, o, "\n")
	}
	re := regexp.MustCompile(`^`)

	matches, err := o.GrepLimit(re, 1<<16, 5)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 5 {
		t.Fatalf("matches = %d, want cap of 5", len(matches))
	}
}

func TestOutputGrepFinalLineWithoutNewlineOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendOutput(t, o, "starting\nalmost ready\nready")
	re := regexp.MustCompile(`^ready$`)

	matches, err := o.Grep(re, 1<<16)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "ready" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].ByteOffset != int64(len("starting\nalmost ready\n")) {
		t.Errorf("byte offset = %d, want %d", matches[0].ByteOffset, len("starting\nalmost ready\n"))
	}
}

func TestOutputGrepLimitSkipsOverlongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	overlong := strings.Repeat("x", 8192) + "ready\n"
	appendOutput(t, o, overlong+"later ready\n")
	re := regexp.MustCompile(`ready`)

	matches, err := o.GrepLimitLineBytes(re, 1024, 10, 64)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "later ready" {
		t.Fatalf("matches = %+v, want only bounded later line", matches)
	}
	if matches[0].ByteOffset != int64(len(overlong)) {
		t.Fatalf("byte offset = %d, want %d", matches[0].ByteOffset, len(overlong))
	}
}
