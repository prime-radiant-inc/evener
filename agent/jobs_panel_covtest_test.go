package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestClampJobTailBytes covers all three branches of clampJobTailBytes
// (lines 30-36).
func TestClampJobTailBytes(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, jobOutputTailDefaultBytes},
		{"negative", -1, jobOutputTailDefaultBytes},
		{"max exceeded", jobOutputTailMaxBytes + 100, jobOutputTailMaxBytes},
		{"exact max", jobOutputTailMaxBytes, jobOutputTailMaxBytes},
		{"normal", 1024, 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampJobTailBytes(tc.in); got != tc.want {
				t.Fatalf("clampJobTailBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsOutputNotExistErr covers the error-checking helper.
func TestIsOutputNotExistErr(t *testing.T) {
	if !isOutputNotExistErr(os.ErrNotExist) {
		t.Fatal("expected true for os.ErrNotExist")
	}
	if !isOutputNotExistErr(fmt.Errorf("wrapped: %w", os.ErrNotExist)) {
		t.Fatal("expected true for wrapped ErrNotExist")
	}
	if isOutputNotExistErr(errors.New("other error")) {
		t.Fatal("expected false for non-NotExist error")
	}
}

// TestJobOutputTailFromWindow covers the conversion function (lines 61-69).
func TestJobOutputTailFromWindow(t *testing.T) {
	w := jobOutputWindow{
		content:  "hello",
		start:    0,
		end:      5,
		total:    5,
		earliest: 0,
	}
	got := jobOutputTailFromWindow(w)
	if got.Tail != "hello" || got.TotalBytes != 5 || got.Truncated {
		t.Fatalf("got = %+v", got)
	}

	// Truncated when start > 0.
	w.start = 3
	got = jobOutputTailFromWindow(w)
	if !got.Truncated {
		t.Fatal("expected truncated when start > 0")
	}
	if got.RetainedStart != 3 {
		t.Fatalf("RetainedStart = %d, want 3", got.RetainedStart)
	}

	// HasEarlier when start > earliest.
	w.earliest = 0
	if !got.HasEarlier {
		t.Fatal("expected HasEarlier when start > earliest")
	}
}

// TestJobOutputTail_NilSession covers the nil-session guard (line 45-46).
func TestJobOutputTail_NilSession(t *testing.T) {
	var s *Session
	_, found, err := s.JobOutputTail("job1", 0, 0)
	if found || err != nil {
		t.Fatalf("nil session: found=%v err=%v, want false nil", found, err)
	}
}

// TestLoadSessionJobOutputTail_InvalidSessionID covers the session-ID
// validation error (line 77-78).
func TestLoadSessionJobOutputTail_InvalidSessionID(t *testing.T) {
	_, _, err := LoadSessionJobOutputTail(t.TempDir(), "../escaped", "job1", 0, 0)
	if err == nil {
		t.Fatal("expected error for invalid session ID")
	}
}

// TestLoadSessionJobOutputTail_NoJobsFile covers the missing-jobs-file path
// (line 82-83).
func TestLoadSessionJobOutputTail_NoJobsFile(t *testing.T) {
	_, found, err := LoadSessionJobOutputTail(t.TempDir(), "sess123", "job1", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing jobs file")
	}
}

// TestLoadSessionJobOutputTail_ReadError covers the ReadEvents error path
// (line 87-89).
func TestLoadSessionJobOutputTail_ReadError(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sessreaderr"
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	os.MkdirAll(filepath.Dir(jobsPath), 0o755)
	os.WriteFile(jobsPath, []byte("not valid jsonl\n"), 0o644)
	_, _, err := LoadSessionJobOutputTail(dir, sessionID, "job1", 0, 0)
	if err == nil {
		t.Fatal("expected error for malformed jobs.jsonl")
	}
}
