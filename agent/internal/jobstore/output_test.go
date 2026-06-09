package jobstore

import (
	"errors"
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
