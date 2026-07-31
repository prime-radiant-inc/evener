package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/identifier"
)

func TestSummarizeJobRecordShell(t *testing.T) {
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	exit := 0
	rec := &jobstore.JobRecord{
		JobID:       "job_1",
		Type:        jobstore.JobShell,
		Status:      jobstore.StatusCompleted,
		Description: "run tests",
		Command:     "go test ./...",
		Background:  true,
		StartedAt:   started,
		ExitCode:    &exit,
		OutputBytes: 123,
		OutputPath:  "/tmp/out.log",
	}
	got := summarizeJobRecord(rec)
	if got.JobID != "job_1" || got.Type != "shell" || got.Status != "completed" {
		t.Errorf("identity fields: %+v", got)
	}
	if got.Description != "run tests" || got.Command != "go test ./..." {
		t.Errorf("description/command: %+v", got)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("exit code: %+v", got)
	}
	if !got.HasOutput {
		t.Error("HasOutput should be true when OutputPath is set")
	}
	if got.EndedAt != "" {
		t.Errorf("EndedAt should be empty for nil EndedAt, got %q", got.EndedAt)
	}
}

func TestSummarizeJobRecordDescriptionFallback(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "job_2", Type: jobstore.JobDelegate, Status: jobstore.StatusRunning, Task: "scout the repo"}
	if got := summarizeJobRecord(rec); got.Description != "scout the repo" {
		t.Errorf("description should fall back to Task, got %q", got.Description)
	}
	rec2 := &jobstore.JobRecord{JobID: "job_3", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Command: "make build"}
	if got := summarizeJobRecord(rec2); got.Description != "make build" {
		t.Errorf("description should fall back to Command, got %q", got.Description)
	}
}

func TestLoadSessionJobListEmptyWhenNoLog(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSessionJobList(dir, identifier.MustNewSessionID())
	if err != nil {
		t.Fatalf("LoadSessionJobList: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %+v", got)
	}
}

func TestLoadSessionJobListOrdersAndProjects(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, id := range []string{"job_a", "job_b"} {
		ts := now.Add(time.Duration(i) * time.Second)
		if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: ts, JobID: id, Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &ts}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionJobList(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionJobList: %v", err)
	}
	if len(got) != 2 || got[0].JobID != "job_a" || got[1].JobID != "job_b" {
		t.Errorf("ordered projection: %+v", got)
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
	st, err := jobstore.Open(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
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

func TestLoadSessionJobOutputTailMissingOutputFile(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
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

func TestSessionJobSummariesAndOutputTailNilManager(t *testing.T) {
	var s *Session
	if got := s.JobSummaries(); got == nil || len(got) != 0 {
		t.Errorf("nil session JobSummaries: %+v", got)
	}
	if _, found, err := s.JobOutputTail("job_1", 0); err != nil || found {
		t.Errorf("nil session JobOutputTail: found=%v err=%v", found, err)
	}
}
