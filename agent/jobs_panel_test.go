package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

func TestProjectActivityJobFields(t *testing.T) {
	t.Parallel()
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

// TestLoadSessionJobOutputTailRejectsUnsafeSessionID is a kata 1gc4 sibling
// site: LoadSessionJobOutputTail joins sessionID into a jobsDir path with no
// validation on the call, so a traversal-shaped ID must be refused before
// that join.
func TestLoadSessionJobOutputTailRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, found, err := LoadSessionJobOutputTail(dir, "../escaped", "job_x", 0, 4)
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("LoadSessionJobOutputTail(%q) error = %v, want schema.ErrInvalidSessionID", "../escaped", err)
	}
	if found {
		t.Errorf("LoadSessionJobOutputTail(%q) found = true, want false", "../escaped")
	}
}

// Paging: beforeBytes reads the window ending at that lifetime offset, and
// HasEarlier tells the client whether another page exists.
func TestLoadSessionJobOutputTailPagesBackwards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
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

	page, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_x", 0, 4)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if page.Tail != "6789" || page.RetainedStart != 6 || !page.HasEarlier {
		t.Fatalf("tail page: %+v, want 6789 at 6 with earlier pages", page)
	}

	page, found, err = LoadSessionJobOutputTail(dir, sessionID, "job_x", page.RetainedStart, 4)
	if err != nil || !found {
		t.Fatalf("middle page: found=%v err=%v", found, err)
	}
	if page.Tail != "2345" || page.RetainedStart != 2 || !page.HasEarlier {
		t.Fatalf("middle page: %+v, want 2345 at 2 with earlier pages", page)
	}

	page, found, err = LoadSessionJobOutputTail(dir, sessionID, "job_x", page.RetainedStart, 4)
	if err != nil || !found {
		t.Fatalf("head page: found=%v err=%v", found, err)
	}
	if page.Tail != "01" || page.RetainedStart != 0 || page.HasEarlier {
		t.Fatalf("head page: %+v, want 01 at 0 with no earlier pages", page)
	}
}

// The panel projection of a multi-byte tail keeps its byte math consistent with
// the bytes it carries: the window start is realigned to a rune boundary, and
// RetainedStart still names the first byte actually sent, so the caption's
// TotalBytes - RetainedStart is exactly the tail's length.
func TestLoadSessionJobOutputTailAlignsMultiByteWindow(t *testing.T) {
	t.Parallel()
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
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_e", 0, 6)
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
	t.Parallel()
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
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_y", 0, 0)
	if err != nil || !found {
		t.Fatalf("missing output file: found=%v err=%v", found, err)
	}
	if tail.Tail != "" || tail.TotalBytes != 0 || tail.Truncated {
		t.Errorf("missing output file should be an empty tail, got %+v", tail)
	}
}

func TestSessionJobOutputTailNilManager(t *testing.T) {
	t.Parallel()
	var s *Session
	if _, found, err := s.JobOutputTail("job_1", 0, 0); err != nil || found {
		t.Errorf("nil session JobOutputTail: found=%v err=%v", found, err)
	}
}

// Session.JobGet resolves a live session's own job record and projects it into
// the activity-tree job shape, so the web UI can render the untruncated command
// beside the job's output.
func TestSessionJobGetReturnsUntruncatedCommand(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	now := time.Now()
	if err := jm.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x",
		Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: "sess_1", VisibleToSession: "sess_1",
		Command: "go test ./... -count=1", Description: "run tests",
		StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	sess := &Session{}
	sess.jobManager = jm

	job, found, err := sess.JobGet("job_x")
	if err != nil || !found {
		t.Fatalf("JobGet: found=%v err=%v", found, err)
	}
	if job.JobID != "job_x" || job.Command != "go test ./... -count=1" || job.Description != "run tests" {
		t.Errorf("job = %+v", job)
	}
	if job.OwnerRef != "local:sess_1" {
		t.Errorf("OwnerRef = %q, want local:sess_1", job.OwnerRef)
	}
}

func TestSessionJobGetNotFound(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	sess := &Session{}
	sess.jobManager = jm
	job, found, err := sess.JobGet("job_missing")
	if err != nil || found {
		t.Fatalf("JobGet(missing): found=%v err=%v, want found=false err=nil", found, err)
	}
	if job.JobID != "" {
		t.Errorf("job = %+v, want zero value", job)
	}
}

func TestSessionJobGetNilManager(t *testing.T) {
	t.Parallel()
	var s *Session
	if _, found, err := s.JobGet("job_1"); err != nil || found {
		t.Errorf("nil session JobGet: found=%v err=%v", found, err)
	}
}

// LoadSessionJobGet reads one persisted session's jobs.jsonl for the hub's
// past-session fallback and projects the record's untruncated command.
func TestLoadSessionJobGetReturnsUntruncatedCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	if err := os.MkdirAll(jobsDir(dir, sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x",
		Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: sessionID, VisibleToSession: sessionID,
		Command: "make build -j8", Description: "build",
		StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	job, found, err := LoadSessionJobGet(dir, sessionID, "job_x")
	if err != nil || !found {
		t.Fatalf("LoadSessionJobGet: found=%v err=%v", found, err)
	}
	if job.JobID != "job_x" || job.Command != "make build -j8" {
		t.Errorf("job = %+v", job)
	}
	if job.OwnerRef != "local:"+sessionID {
		t.Errorf("OwnerRef = %q, want local:%s", job.OwnerRef, sessionID)
	}
}

func TestLoadSessionJobGetNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	if err := os.MkdirAll(jobsDir(dir, sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_other",
		Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, found, err := LoadSessionJobGet(dir, sessionID, "job_missing")
	if err != nil || found {
		t.Fatalf("LoadSessionJobGet(missing): found=%v err=%v, want found=false err=nil", found, err)
	}
}

// LoadSessionJobGet joins sessionID into a jobsDir path, so a traversal-shaped
// ID must be refused before that join.
func TestLoadSessionJobGetRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, found, err := LoadSessionJobGet(dir, "../escaped", "job_x")
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("LoadSessionJobGet(%q) error = %v, want schema.ErrInvalidSessionID", "../escaped", err)
	}
	if found {
		t.Errorf("LoadSessionJobGet(%q) found = true, want false", "../escaped")
	}
}

// recordForRead hands out a SNAPSHOT of the live record, never the running
// record itself. Session.JobGet projects that record after the lock is
// released, while the job path mutates the live one in place under the lock
// (finalizeJob, stampLastActivityLocked, noteJobActivity), so handing out the
// live pointer is a data race the -race lane can flag and can yield a torn
// snapshot. Every other live-record reader in jobs.go clones under the lock
// (liveJobRecords, cloneJobRecord).
func TestRecordForReadSnapshotsTheLiveRecord(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	jm.mu.Lock()
	live := jm.running[rec.JobID]
	jm.mu.Unlock()
	if live == nil {
		t.Fatal("the job under test is not running")
	}

	_, got, err := jm.recordForRead(rec.JobID)
	if err != nil || got == nil {
		t.Fatalf("recordForRead: rec=%v err=%v", got, err)
	}
	if got == live.rec {
		t.Fatal("recordForRead returned the live record by pointer; its caller reads it after the lock is released")
	}
	if got.JobID != rec.JobID {
		t.Errorf("snapshot JobID = %q, want %q", got.JobID, rec.JobID)
	}
}
