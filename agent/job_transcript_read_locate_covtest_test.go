package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

// TestLocateLocalJob_InvalidBucket covers the invalid-bucket-dir error path
// in locateLocalJob (line 41-43): a state dir with an invalid project bucket
// name returns an error.
func TestLocateLocalJob_InvalidBucket(t *testing.T) {
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := identifier.NewJobID(sid)
	if err != nil {
		t.Fatal(err)
	}
	// A state dir that looks like an evener/projects/ layout but with an invalid
	// project id (contains a space, which ValidateProjectID rejects).
	base := t.TempDir()
	badBucket := filepath.Join(base, "evener", "projects", "invalid bucket")
	if err := os.MkdirAll(badBucket, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = locateLocalJob(badBucket, jobID)
	if err == nil {
		t.Fatal("expected error for invalid bucket dir")
	}
}

// TestReadLocalJobSnapshot_ChangedDuringRead covers the
// ErrOutputChangedDuringRead path in readLocalJobSnapshot (line 256-257).
func TestReadLocalJobSnapshot_ChangedDuringRead(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	// Write a file that will be changed during read. We use a hook to inject
	// the error: readLocalJobOutputSnapshot is a package-level var.
	original := readLocalJobOutputSnapshot
	t.Cleanup(func() { readLocalJobOutputSnapshot = original })
	readLocalJobOutputSnapshot = func(path string, readBytes int, toleratePartialTail bool) (jobstore.OutputSnapshot, error) {
		return jobstore.OutputSnapshot{}, jobstore.ErrOutputChangedDuringRead
	}
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := identifier.NewJobID(sid)
	if err != nil {
		t.Fatal(err)
	}
	// Create a minimal session dir structure so locateLocalJobRetainedTarget
	// succeeds before the snapshot read fails.
	bucket := filepath.Join(dir, "evener", "projects", "Project-test-0123456789")
	sessDir := filepath.Join(bucket, "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a jobs.jsonl with a job_started and job_finished for our job.
	writeJobEventsForTest(t, sessDir, jobID, outputPath)
	_, err = readLocalJobSnapshot(bucket, jobID, 100)
	if err == nil {
		t.Fatal("expected changed-during-read error")
	}
}

// TestReadLocalJobSnapshot_NotExist covers the os.ErrNotExist path in
// readLocalJobSnapshot (line 259-260).
func TestReadLocalJobSnapshot_NotExist(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	original := readLocalJobOutputSnapshot
	t.Cleanup(func() { readLocalJobOutputSnapshot = original })
	readLocalJobOutputSnapshot = func(path string, readBytes int, toleratePartialTail bool) (jobstore.OutputSnapshot, error) {
		return jobstore.OutputSnapshot{}, os.ErrNotExist
	}
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := identifier.NewJobID(sid)
	if err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Join(dir, "evener", "projects", "Project-test-0123456789")
	sessDir := filepath.Join(bucket, "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJobEventsForTest(t, sessDir, jobID, outputPath)
	_, err = readLocalJobSnapshot(bucket, jobID, 100)
	if err == nil {
		t.Fatal("expected not-exist error")
	}
}

// TestReadLocalJobSnapshot_GenericError covers the generic-error path in
// readLocalJobSnapshot (line 262-263).
func TestReadLocalJobSnapshot_GenericError(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	original := readLocalJobOutputSnapshot
	t.Cleanup(func() { readLocalJobOutputSnapshot = original })
	customErr := errJobNotFound("test_job")
	readLocalJobOutputSnapshot = func(path string, readBytes int, toleratePartialTail bool) (jobstore.OutputSnapshot, error) {
		return jobstore.OutputSnapshot{}, customErr
	}
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := identifier.NewJobID(sid)
	if err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Join(dir, "evener", "projects", "Project-test-0123456789")
	sessDir := filepath.Join(bucket, "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJobEventsForTest(t, sessDir, jobID, outputPath)
	_, err = readLocalJobSnapshot(bucket, jobID, 100)
	if err == nil {
		t.Fatal("expected generic error")
	}
}

// writeJobEventsForTest writes a minimal jobs.jsonl with a completed job so
// locateLocalJobRetainedTarget can find the job.
func writeJobEventsForTest(t *testing.T, sessDir, jobID, outputPath string) {
	t.Helper()
	jobsDir := filepath.Join(sessDir, "jobs.jsonl")
	events := []byte(`{"kind":"job_started","seq":1,"job_id":"` + jobID + `","type":"shell","command":"echo hi","output_path":"` + outputPath + `"}
{"kind":"job_finished","seq":2,"job_id":"` + jobID + `","status":"completed","exit_code":0,"output_bytes":0}
`)
	if err := os.WriteFile(jobsDir, events, 0o644); err != nil {
		t.Fatal(err)
	}
}
