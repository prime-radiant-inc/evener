package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestSessionJobOutputTail_JobNotFound covers the isJobNotFoundErr branch
// (jobs_panel.go:50-52): a missing job id returns found=false, no error.
func TestSessionJobOutputTail_JobNotFound(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{jobManager: jm}
	_, found, err := s.JobOutputTail("job_does_not_exist", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing job")
	}
}

// TestSessionJobOutputTail_OutputNotExist covers the isOutputNotExistErr
// branch (jobs_panel.go:53-55): a job whose output file does not exist yet
// returns found=true with an empty tail and no error.
func TestSessionJobOutputTail_OutputNotExist(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	// Finalize the job so it goes through the store path, but do not create
	// an output file on disk.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Remove the output file so validatedOutputStatsForRecord returns ErrNotExist.
	if jm.running[rec.JobID] != nil && jm.running[rec.JobID].output != nil {
		_ = jm.running[rec.JobID].output.Close()
	}
	s := &Session{jobManager: jm}
	tail, found, err := s.JobOutputTail(rec.JobID, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true (job exists)")
	}
	if tail.Tail != "" {
		t.Fatalf("expected empty tail, got %q", tail.Tail)
	}
}

// TestSessionJobOutputTail_Success covers the success path
// (jobs_panel.go:58): a live job with output returns the tail content.
func TestSessionJobOutputTail_Success(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := []byte("hello world output\n")
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, output); err != nil {
		t.Fatalf("append: %v", err)
	}
	s := &Session{jobManager: jm}
	tail, found, err := s.JobOutputTail(rec.JobID, 0, 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !strings.Contains(tail.Tail, "hello world") {
		t.Fatalf("tail = %q, want output content", tail.Tail)
	}
}
