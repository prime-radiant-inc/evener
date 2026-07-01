package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// newJobManager surfaces the jobstore open error when the durable log path is
// occupied by a directory rather than a file.
func TestW2Dlg_NewJobManager_StoreOpenError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dir := jobsDir(stateDir, "S")
	if err := os.MkdirAll(filepath.Join(dir, "jobs.jsonl"), 0o755); err != nil {
		t.Fatalf("seed dir-as-log: %v", err)
	}
	if _, err := newJobManager(stateDir, "S", func(jobNotification) {}); err == nil {
		t.Fatal("newJobManager with dir-shaped log: want open error")
	}
}

// newJobManager surfaces the watch-send restore error when the durable log is
// corrupt.
func TestW2Dlg_NewJobManager_RestoreError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dir := jobsDir(stateDir, "S")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s1cov_corruptJobLog(t, filepath.Join(dir, "jobs.jsonl"))
	if _, err := newJobManager(stateDir, "S", func(jobNotification) {}); err == nil {
		t.Fatal("newJobManager with corrupt log: want restore error")
	}
}

// resumedDelegateRestoreDescriptor defaults an unversioned predecessor to
// version 1 while carrying the parent/owner coordinates from the session.
func TestW2Dlg_ResumedDelegateRestoreDescriptor_DefaultsVersion(t *testing.T) {
	t.Parallel()
	s := newDelegateTestSession(t, nil)
	previous := &jobstore.DelegateRestoreDescriptor{
		Version:      0,
		Task:         "carried task",
		OriginTurnID: "turn_1",
	}
	desc := s.resumedDelegateRestoreDescriptor("job_1", "child_1", "ref_1", nil, previous)
	if desc.Version != 1 {
		t.Fatalf("version = %d, want 1", desc.Version)
	}
	if desc.Task != "carried task" || desc.OriginTurnID != "turn_1" {
		t.Fatalf("carry-through wrong: %+v", desc)
	}
	if desc.ParentSessionID != s.ID() || desc.OwnerSessionID != s.ID() {
		t.Fatalf("owner/parent = (%q, %q), want %q", desc.ParentSessionID, desc.OwnerSessionID, s.ID())
	}
	if desc.ChildSessionID != "child_1" || desc.ParentJobID != "job_1" {
		t.Fatalf("child/job coords wrong: %+v", desc)
	}
}
