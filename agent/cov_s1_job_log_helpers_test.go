package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func s1cov_writeJobLog(t *testing.T, stateDir, sessID string, events ...jobstore.Event) string {
	t.Helper()
	dir := jobsDir(stateDir, sessID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jobs.jsonl")
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	for _, event := range events {
		if err := store.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close jobstore: %v", err)
	}
	return path
}

func s1cov_corruptJobLog(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open for corrupt: %v", err)
	}
	if _, err := f.WriteString("{not valid json\n"); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestS1Cov_LoadSessionHistoricalJobRecords_FoldsRecords(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	now := time.Now().UTC()
	out := int64(4096)
	s1cov_writeJobLog(t, stateDir, "SESS",
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_d1", Type: jobstore.JobDelegate, OwnerSessionID: "SESS", StartedAt: &now, DelegateID: "dlg_1", Task: "do the thing", OriginTurnID: "turn_1", OriginToolCallID: "call_1", OriginItemID: "item_1"},
		jobstore.Event{Kind: jobstore.EventJobSessionAssigned, TS: now, JobID: "job_d1", TranscriptRef: encodeRef("", "CHILD")},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_d1", Status: jobstore.StatusCompleted, Reason: "done", OutputBytes: out, EndedAt: &now},
	)
	got, err := LoadSessionHistoricalJobRecords(stateDir, "SESS")
	if err != nil {
		t.Fatalf("LoadSessionHistoricalJobRecords: %v", err)
	}
	rec, ok := got["job_d1"]
	if !ok || rec.Type != string(jobstore.JobDelegate) || rec.Status != string(jobstore.StatusCompleted) || rec.DelegateID != "dlg_1" || rec.Task != "do the thing" || rec.Reason != "done" || rec.OriginTurnID != "turn_1" || rec.OriginToolCallID != "call_1" || rec.OriginItemID != "item_1" || rec.TranscriptRef != encodeRef("", "CHILD") || rec.OutputBytes != out {
		t.Fatalf("historical record = %+v", rec)
	}
}

func TestS1Cov_LoadSessionHistoricalJobRecords_MissingLogEmpty(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	got, err := LoadSessionHistoricalJobRecords(stateDir, "NOLOG")
	if err != nil || len(got) != 0 {
		t.Fatalf("missing log = %v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(jobsDir(stateDir, "NOLOG"), "jobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("missing log created a file: %v", err)
	}
}

func TestS1Cov_LoadSessionHistoricalJobRecords_CorruptLogErrors(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	now := time.Now().UTC()
	path := s1cov_writeJobLog(t, stateDir, "SESS", jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x", Type: jobstore.JobShell, OwnerSessionID: "SESS", StartedAt: &now})
	s1cov_corruptJobLog(t, path)
	if _, err := LoadSessionHistoricalJobRecords(stateDir, "SESS"); err == nil {
		t.Fatal("corrupt log must return an error")
	}
}
