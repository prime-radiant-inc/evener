package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/identifier"
)

func TestProjectActivityJobFields(t *testing.T) {
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.FixedZone("offset", 2*60*60))
	ended := started.Add(time.Minute)
	exit := 1
	rec := &jobstore.JobRecord{
		JobID:          "job_1",
		OwnerSessionID: "child",
		Type:           jobstore.JobShell,
		Status:         jobstore.StatusFailed,
		Reason:         "exit_status",
		Description:    "run tests",
		Command:        "go test ./...",
		Background:     true,
		StartedAt:      started,
		EndedAt:        &ended,
		ExitCode:       &exit,
		OutputBytes:    123,
		OutputPath:     "/tmp/out.log",
	}
	got := projectActivityJob(rec, "local:child")
	if got.JobID != "job_1" || got.OwnerSessionID != "child" || got.OwnerRef != "local:child" {
		t.Errorf("identity/owner fields: %+v", got)
	}
	if got.Type != "shell" || got.Status != "failed" || !got.Terminal || got.Outcome != "failure" {
		t.Errorf("lifecycle fields: %+v", got)
	}
	if got.Description != "run tests" || got.Command != "go test ./..." || got.Reason != "exit_status" {
		t.Errorf("description fields: %+v", got)
	}
	if !got.Background || !got.HasOutput || got.OutputBytes != 123 || got.ExitCode == nil || *got.ExitCode != 1 {
		t.Errorf("runtime fields: %+v", got)
	}
	if got.StartedAt != started.UTC().Format(time.RFC3339) || got.EndedAt != ended.UTC().Format(time.RFC3339) {
		t.Errorf("timestamps: %+v", got)
	}
}

func TestLoadSessionJobOutputTail(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	// Write a store with one finished job whose OutputPath points at a file.
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(logDir, "job_x.log")
	if err := os.WriteFile(outPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_x", Status: jobstore.StatusCompleted, OutputBytes: 10, TerminalGen: "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_x", 4)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if tail.Tail != "6789" || tail.TotalBytes != 10 || !tail.Truncated {
		t.Errorf("tail: %+v", tail)
	}
	if _, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_nope", 4); err != nil || found {
		t.Errorf("unknown job: found=%v err=%v", found, err)
	}
}

// The panel projection of a multi-byte tail keeps its byte math consistent with
// the bytes it carries: the window start is realigned to a rune boundary, and
// RetainedStart still names the first byte actually sent, so the caption's
// TotalBytes - RetainedStart is exactly the tail's length.
func TestLoadSessionJobOutputTailAlignsMultiByteWindow(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three 4-byte emoji: a 6-byte window opens 2 bytes into the second one.
	outPath := filepath.Join(logDir, "job_e.log")
	if err := os.WriteFile(outPath, []byte("😀😀😀"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_e", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_e", Status: jobstore.StatusCompleted, OutputBytes: 12, TerminalGen: "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_e", 6)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if tail.Tail != "😀" || tail.TotalBytes != 12 || !tail.Truncated {
		t.Errorf("tail: %+v", tail)
	}
	if tail.RetainedStart != 8 || tail.TotalBytes-tail.RetainedStart != int64(len(tail.Tail)) {
		t.Errorf("caption math: %+v carries %d bytes", tail, len(tail.Tail))
	}
}

func TestLoadSessionJobOutputTailMissingOutputFile(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_y", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// No output file was ever written: the default <jobs>/<id>.log is absent.
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_y", 0)
	if err != nil || !found {
		t.Fatalf("missing output file: found=%v err=%v", found, err)
	}
	if tail.Tail != "" || tail.TotalBytes != 0 || tail.Truncated {
		t.Errorf("missing output file should be an empty tail, got %+v", tail)
	}
}

func TestSessionJobOutputTailNilManager(t *testing.T) {
	var s *Session
	if _, found, err := s.JobOutputTail("job_1", 0); err != nil || found {
		t.Errorf("nil session JobOutputTail: found=%v err=%v", found, err)
	}
}
