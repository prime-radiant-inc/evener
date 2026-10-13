package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestClampJobTailBytes covers all three branches of clampJobPageBytes
// (lines 30-36).
func TestClampJobTailBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   int64
		want int
	}{
		{"zero", 0, jobOutputPageDefaultBytes},
		{"negative", -1, jobOutputPageDefaultBytes},
		{"max exceeded", jobOutputPageMaxBytes + 100, jobOutputPageMaxBytes},
		{"exact max", jobOutputPageMaxBytes, jobOutputPageMaxBytes},
		{"normal", 1024, 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampJobPageBytes(tc.in); got != tc.want {
				t.Fatalf("clampJobPageBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsOutputNotExistErr covers the error-checking helper.
func TestIsOutputNotExistErr(t *testing.T) {
	t.Parallel()
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

// TestJobOutputPageFromWindow keeps selected offsets distinct from storage bounds.
func TestJobOutputPageFromWindow(t *testing.T) {
	t.Parallel()
	w := jobstore.OutputWindowSnapshot{
		Content: []byte("hello"), Start: 0, End: 5, TotalBytes: 5, RetainedStart: 0,
	}
	got, found, err := jobOutputPageResult(w, true, nil)
	if err != nil || !found || got.Data != "hello" || got.TotalBytes != 5 || got.OffsetBytes != 0 || got.BytesReturned != 5 || got.Encoding != "utf8" {
		t.Fatalf("got = %+v", got)
	}

	w.Start, w.End, w.TotalBytes = 3, 8, 8
	w.RetainedStart = 1
	got, found, err = jobOutputPageResult(w, true, nil)
	if err != nil || !found || got.OffsetBytes != 3 || got.BytesReturned != 5 || got.RetainedStartBytes != 1 || got.OffsetBytes <= got.RetainedStartBytes {
		t.Fatalf("earlier retained output bounds = %+v, found=%v err=%v", got, found, err)
	}
}

// TestJobOutputPage_NilSession covers the nil-session guard (line 45-46).
func TestJobOutputPage_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	_, found, err := s.JobOutputPage("job1", nil, 0)
	if found || err != nil {
		t.Fatalf("nil session: found=%v err=%v, want false nil", found, err)
	}
}

// TestLoadSessionJobOutputPage_InvalidSessionID covers the session-ID
// validation error (line 77-78).
func TestLoadSessionJobOutputPage_InvalidSessionID(t *testing.T) {
	t.Parallel()
	_, _, err := LoadSessionJobOutputPage(t.TempDir(), "../escaped", "job1", nil, 0)
	if err == nil {
		t.Fatal("expected error for invalid session ID")
	}
}

// TestLoadSessionJobOutputPage_NoJobsFile covers the missing-jobs-file path
// (line 82-83).
func TestLoadSessionJobOutputPage_NoJobsFile(t *testing.T) {
	t.Parallel()
	_, found, err := LoadSessionJobOutputPage(t.TempDir(), "sess123", "job1", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing jobs file")
	}
}

// TestLoadSessionJobOutputPage_ReadError covers the ReadEvents error path
// (line 87-89).
func TestLoadSessionJobOutputPage_ReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := "sessreaderr"
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	os.MkdirAll(filepath.Dir(jobsPath), 0o755)
	os.WriteFile(jobsPath, []byte("not valid jsonl\n"), 0o644)
	_, _, err := LoadSessionJobOutputPage(dir, sessionID, "job1", nil, 0)
	if err == nil {
		t.Fatal("expected error for malformed jobs.jsonl")
	}
}
