package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

// TestLoadSessionJobOutputTail_TerminalRecordMismatch covers the error path at
// line 105: validatedOutputStatsForRecord returns a non-NotExist error because
// the terminal record's OutputBytes does not match the file's actual size.
func TestLoadSessionJobOutputTail_TerminalRecordMismatch(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(logDir, "job_mismatch.log")
	if err := os.WriteFile(outPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_mismatch", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	// Terminal record claims 999 bytes but file has 10.
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_mismatch", Status: jobstore.StatusCompleted, OutputBytes: 999, TerminalGen: "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	_, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_mismatch", 0, 4)
	if err == nil {
		t.Fatal("expected error for terminal record mismatch")
	}
	if !found {
		t.Fatal("expected found=true (job exists)")
	}
}

// TestLoadSessionJobOutputTail_OutputIsDirectory covers the error path at
// lines 109-112: windowOutputFile returns a non-NotExist error because the
// output path is a directory (read fails).
func TestLoadSessionJobOutputTail_OutputIsDirectory(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a directory where the output file should be.
	outPath := filepath.Join(logDir, "job_dir.log")
	if err := os.Mkdir(outPath, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Use a running (non-terminal) job so validatedOutputStatsForRecord
	// does not enforce OutputBytes matching.
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_dir", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	_, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_dir", 0, 4)
	if err == nil {
		t.Fatal("expected error for directory-as-output")
	}
	if !found {
		t.Fatal("expected found=true (job exists)")
	}
}

// TestLoadSessionJobOutputTail_NoOutputFile covers the not-exist path at
// line 102-103: validatedOutputStatsForRecord returns os.ErrNotExist for a
// running job whose output file does not exist.
func TestLoadSessionJobOutputTail_NoOutputFile(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Do NOT create the output file.
	outPath := filepath.Join(logDir, "job_missing.log")
	st, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_missing", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_missing", 0, 4)
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

// TestLoadSessionJobOutputTail_EmptyOutputPath covers the default-path path at
// line 98: rec.OutputPath is empty, so outPath is built from stateDir/sessionID/jobs.
func TestLoadSessionJobOutputTail_EmptyOutputPath(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the default-named output file.
	outPath := filepath.Join(logDir, "job_default.log")
	if err := os.WriteFile(outPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Start a job with NO OutputPath — the code builds one from the default.
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_default", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_default", 0, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if tail.Tail != "hello" {
		t.Fatalf("tail = %q, want 'hello'", tail.Tail)
	}
}
