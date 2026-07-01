package agent

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// w2dlg_writeFile writes content to a fresh temp file and returns its path.
func w2dlg_writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// A negative maxBytes is rejected with ErrInvalidLimit before any file access.
func TestW2Dlg_TailOutputFile_NegativeBytes(t *testing.T) {
	t.Parallel()
	if _, _, _, err := tailOutputFile("/nonexistent", -1, 0); !errors.Is(err, jobstore.ErrInvalidLimit) {
		t.Fatalf("tailOutputFile(-1) err = %v, want ErrInvalidLimit", err)
	}
	if _, _, _, err := headOutputFile("/nonexistent", -1, 0); !errors.Is(err, jobstore.ErrInvalidLimit) {
		t.Fatalf("headOutputFile(-1) err = %v, want ErrInvalidLimit", err)
	}
}

// A missing output file surfaces an open error.
func TestW2Dlg_TailHeadOutputFile_OpenError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone.log")
	if _, _, _, err := tailOutputFile(missing, 10, 0); err == nil {
		t.Fatal("tailOutputFile missing path: want error")
	}
	if _, _, _, err := headOutputFile(missing, 10, 0); err == nil {
		t.Fatal("headOutputFile missing path: want error")
	}
}

// When the record's total exceeds the retained bytes on disk, the read is marked
// truncated even though the whole retained file fits inside the requested window.
func TestW2Dlg_TailHeadOutputFile_TotalExceedsRetained(t *testing.T) {
	t.Parallel()
	path := w2dlg_writeFile(t, "abc")

	out, total, truncated, err := tailOutputFile(path, 100, 999)
	if err != nil {
		t.Fatalf("tailOutputFile: %v", err)
	}
	if out != "abc" || total != 999 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (abc, 999, true)", out, total, truncated)
	}

	out, total, truncated, err = headOutputFile(path, 100, 999)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if out != "abc" || total != 999 || !truncated {
		t.Fatalf("head = (%q, %d, %v), want (abc, 999, true)", out, total, truncated)
	}
}

// The tail window slides to the last maxBytes and the head window keeps the
// first maxBytes, both flagging truncation when the retained file is larger.
func TestW2Dlg_TailHeadOutputFile_WindowTruncation(t *testing.T) {
	t.Parallel()
	path := w2dlg_writeFile(t, "abcdefghij")

	out, total, truncated, err := tailOutputFile(path, 4, 10)
	if err != nil {
		t.Fatalf("tailOutputFile: %v", err)
	}
	if out != "ghij" || total != 10 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (ghij, 10, true)", out, total, truncated)
	}

	out, total, truncated, err = headOutputFile(path, 4, 10)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if out != "abcd" || total != 10 || !truncated {
		t.Fatalf("head = (%q, %d, %v), want (abcd, 10, true)", out, total, truncated)
	}
}

// An unknown job id surfaces the not-found error across every read path when no
// running job and no durable record exists.
func TestW2Dlg_ReadPaths_UnknownJobNotFound(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	if _, _, _, err := jm.readOutput("job_missing", 10); err == nil {
		t.Fatal("readOutput unknown: want not-found error")
	}
	if _, _, _, err := jm.readOutputHead("job_missing", 10); err == nil {
		t.Fatal("readOutputHead unknown: want not-found error")
	}
	if _, err := jm.outputDropped("job_missing"); err == nil {
		t.Fatal("outputDropped unknown: want not-found error")
	}
	if _, err := jm.grepOutput("job_missing", regexp.MustCompile("x")); err == nil {
		t.Fatal("grepOutput unknown: want not-found error")
	}
}

// A corrupt jobs.jsonl fails the store load underneath every read path.
func TestW2Dlg_ReadPaths_StoreLoadError(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))

	if _, _, _, err := jm.readOutput("job_x", 10); err == nil {
		t.Fatal("readOutput corrupt store: want error")
	}
	if _, _, _, err := jm.readOutputHead("job_x", 10); err == nil {
		t.Fatal("readOutputHead corrupt store: want error")
	}
	if _, err := jm.outputDropped("job_x"); err == nil {
		t.Fatal("outputDropped corrupt store: want error")
	}
	if _, err := jm.grepOutput("job_x", regexp.MustCompile("x")); err == nil {
		t.Fatal("grepOutput corrupt store: want error")
	}
}
