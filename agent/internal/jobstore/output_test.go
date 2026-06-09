package jobstore

import (
	"path/filepath"
	"regexp"
	"testing"
)

func TestOutputAppendAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, err := OpenOutput(path, 1024)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = o.Append([]byte("line1\n"))
	_, _ = o.Append([]byte("line2\n"))

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
	o, _ := OpenOutput(path, 1<<20)
	_, _ = o.Append([]byte("aaaaaXXXXX")) // 10 bytes
	data, total, truncated, _ := o.Tail(4)
	if string(data) != "XXXX" {
		t.Errorf("tail(4) = %q, want last 4 bytes", data)
	}
	if total != 10 || !truncated {
		t.Errorf("total=%d truncated=%v, want 10/true", total, truncated)
	}
}

func TestOutputGrepReturnsMatchesWithOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_A.log")
	o, _ := OpenOutput(path, 1<<20)
	_, _ = o.Append([]byte("starting\nserver ready\ndone\n"))
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
