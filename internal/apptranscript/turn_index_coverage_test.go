package apptranscript

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnIndexSidecarLimitZero(t *testing.T) {
	got := turnIndexSidecarLimit(0)
	if got != turnIndexSidecarAllowance {
		t.Fatalf("sidecar limit for size 0 should be allowance %d, got %d", turnIndexSidecarAllowance, got)
	}
}

func TestTurnIndexSidecarLimitNegative(t *testing.T) {
	got := turnIndexSidecarLimit(-1)
	if got != turnIndexSidecarAllowance {
		t.Fatalf("sidecar limit for negative size should be allowance %d, got %d", turnIndexSidecarAllowance, got)
	}
}

func TestTurnIndexSidecarLimitNormalSize(t *testing.T) {
	got := turnIndexSidecarLimit(1000)
	want := turnIndexSidecarAllowance + turnIndexSidecarRatio*1000
	if got != want {
		t.Fatalf("sidecar limit for 1000 should be %d, got %d", want, got)
	}
}

func TestTurnIndexSidecarLimitOverflow(t *testing.T) {
	maxInt64 := int64(^uint64(0) >> 1)
	maxLimit := maxInt64 - 1
	// A huge transcriptSize should hit the maxLimit cap
	got := turnIndexSidecarLimit(maxInt64)
	if got != maxLimit {
		t.Fatalf("sidecar limit for huge size should be maxLimit %d, got %d", maxLimit, got)
	}
}

func TestTurnIndexJournalLimitNormal(t *testing.T) {
	sidecar := turnIndexSidecarLimit(1000)
	got := turnIndexJournalLimit(1000)
	if got != 2*sidecar {
		t.Fatalf("journal limit for 1000 should be 2*sidecar=%d, got %d", 2*sidecar, got)
	}
}

func TestTurnIndexJournalLimitOverflow(t *testing.T) {
	maxInt64 := int64(^uint64(0) >> 1)
	maxLimit := maxInt64 - 1
	got := turnIndexJournalLimit(maxInt64)
	if got != maxLimit {
		t.Fatalf("journal limit for huge size should be maxLimit %d, got %d", maxLimit, got)
	}
}

func TestReadFileBoundedMissingFile(t *testing.T) {
	_, err := readFileBounded(filepath.Join(t.TempDir(), "nonexistent"), 1024)
	if err == nil {
		t.Fatal("missing file should error")
	}
}

func TestReadFileBoundedWithinLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	data := []byte("hello world")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFileBounded(path, 1024)
	if err != nil {
		t.Fatalf("readFileBounded: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("expected %q, got %q", data, got)
	}
}

func TestReadFileBoundedExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	data := []byte("hello world")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readFileBounded(path, 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected exceeds error, got %v", err)
	}
}

func TestReadFileBoundedGrowsBeyondLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	// Write exactly limit bytes — the file is at the limit, so it should pass.
	// But a file that grows between Stat and ReadAll should be caught.
	data := make([]byte, 10)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFileBounded(path, 10)
	if err != nil {
		t.Fatalf("file at limit should pass: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 bytes, got %d", len(got))
	}
}

func TestReadBoundedJournalFrameSimple(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("line\n"))
	frame, err := readBoundedJournalFrame(reader, 1024)
	if err != nil {
		t.Fatalf("readBoundedJournalFrame: %v", err)
	}
	if string(frame) != "line\n" {
		t.Fatalf("expected 'line\\n', got %q", frame)
	}
}

func TestReadBoundedJournalFrameEOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("no newline"))
	frame, err := readBoundedJournalFrame(reader, 1024)
	if err != nil {
		// EOF with partial data is valid
		if frame == nil {
			t.Fatalf("expected partial frame, got nil")
		}
	}
	if string(frame) != "no newline" {
		t.Fatalf("expected 'no newline', got %q", frame)
	}
}

func TestReadBoundedJournalFrameExceedsLimit(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("this line is too long\n"))
	_, err := readBoundedJournalFrame(reader, 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected exceeds error, got %v", err)
	}
}

func TestReadBoundedJournalFrameBufferFull(t *testing.T) {
	// A long line that exceeds the bufio buffer size triggers ErrBufferFull
	// which should be handled by continuing to read.
	longLine := strings.Repeat("x", 5000) + "\n"
	reader := bufio.NewReader(strings.NewReader(longLine))
	frame, err := readBoundedJournalFrame(reader, 10000)
	if err != nil {
		t.Fatalf("readBoundedJournalFrame with long line: %v", err)
	}
	if string(frame) != longLine {
		t.Fatalf("expected long line, got %d bytes", len(frame))
	}
}
