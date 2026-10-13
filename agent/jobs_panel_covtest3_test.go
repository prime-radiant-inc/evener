package agent

import (
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// TestSessionJobOutputPage_JobNotFound covers the isJobNotFoundErr branch
// (jobs_panel.go:50-52): a missing job id returns found=false, no error.
func TestSessionJobOutputPage_JobNotFound(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	s := &Session{jobManager: jm}
	_, found, err := s.JobOutputPage("job_does_not_exist", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing job")
	}
}

func TestSessionJobOutputPage_EmptyThenMissingFile(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	// Finalization leaves the already-created empty output file readable.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	s := &Session{jobManager: jm}
	tail, found, err := s.JobOutputPage(rec.JobID, nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true (job exists)")
	}
	if tail.Data != "" || tail.BytesReturned != 0 || tail.OffsetBytes != 0 || tail.TotalBytes != 0 || tail.RetainedStartBytes != 0 || tail.Encoding != "utf8" {
		t.Fatalf("expected an explicit empty page, got %+v", tail)
	}
	if err := os.Remove(jm.outputPathForJob(rec, rec.JobID)); err != nil {
		t.Fatal(err)
	}
	tail, found, err = s.JobOutputPage(rec.JobID, nil, 0)
	var wire appwire.WireError
	if !found || !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || tail != (appwire.JobOutputPage{}) {
		t.Fatalf("missing output: page=%+v found=%v err=%v, want unavailable", tail, found, err)
	}
}

// TestSessionJobOutputPage_Success covers the success path
// (jobs_panel.go:58): a live job with output returns the tail content.
func TestSessionJobOutputPage_Success(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := []byte("hello world output\n")
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, output); err != nil {
		t.Fatalf("append: %v", err)
	}
	s := &Session{jobManager: jm}
	tail, found, err := s.JobOutputPage(rec.JobID, nil, 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !strings.Contains(tail.Data, "hello world") {
		t.Fatalf("tail = %q, want output content", tail.Data)
	}
}
