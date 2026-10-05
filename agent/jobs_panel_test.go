package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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

// TestLoadSessionJobOutputPageRejectsUnsafeSessionID is a kata 1gc4 sibling
// site: LoadSessionJobOutputPage joins sessionID into a jobsDir path with no
// validation on the call, so a traversal-shaped ID must be refused before
// that join.
func TestLoadSessionJobOutputPageRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, found, err := LoadSessionJobOutputPage(dir, "../escaped", "job_x", nil, 4)
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("LoadSessionJobOutputPage(%q) error = %v, want schema.ErrInvalidSessionID", "../escaped", err)
	}
	if found {
		t.Errorf("LoadSessionJobOutputPage(%q) found = true, want false", "../escaped")
	}
}

// Paging ends at beforeBytes; the page offset is distinct from the storage floor.
func TestLoadSessionJobOutputPagePagesBackwards(t *testing.T) {
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

	page, found, err := LoadSessionJobOutputPage(dir, sessionID, "job_x", nil, 4)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if page.Data != "6789" || page.OffsetBytes != 6 || page.BytesReturned != 4 || page.RetainedStartBytes != 0 {
		t.Fatalf("tail page: %+v, want 6789 at 6 with earlier pages", page)
	}

	page, found, err = LoadSessionJobOutputPage(dir, sessionID, "job_x", &page.OffsetBytes, 4)
	if err != nil || !found {
		t.Fatalf("middle page: found=%v err=%v", found, err)
	}
	if page.Data != "2345" || page.OffsetBytes != 2 || page.BytesReturned != 4 || page.RetainedStartBytes != 0 {
		t.Fatalf("middle page: %+v, want 2345 at 2 with earlier pages", page)
	}

	page, found, err = LoadSessionJobOutputPage(dir, sessionID, "job_x", &page.OffsetBytes, 4)
	if err != nil || !found {
		t.Fatalf("head page: found=%v err=%v", found, err)
	}
	if page.Data != "01" || page.OffsetBytes != 0 || page.BytesReturned != 2 || page.RetainedStartBytes != 0 {
		t.Fatalf("head page: %+v, want 01 at 0 with no earlier pages", page)
	}
}

// Raw pages preserve a split scalar; encoding must not alter source-byte counts.
func TestLoadSessionJobOutputPagePreservesMultiByteWindow(t *testing.T) {
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
	tail, found, err := LoadSessionJobOutputPage(dir, sessionID, "job_e", nil, 6)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if string(rawPageBytes(t, tail)) != string([]byte{0x98, 0x80, 0xf0, 0x9f, 0x98, 0x80}) || tail.TotalBytes != 12 || tail.Encoding != "base64" {
		t.Errorf("tail: %+v", tail)
	}
	if tail.OffsetBytes != 6 || tail.BytesReturned != 6 || tail.RetainedStartBytes != 0 {
		t.Errorf("caption math: %+v carries 6 source bytes", tail)
	}
}

func TestLoadSessionJobOutputPageMissingOutputFile(t *testing.T) {
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
	tail, found, err := LoadSessionJobOutputPage(dir, sessionID, "job_y", nil, 0)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || !found {
		t.Fatalf("missing output file: found=%v err=%v", found, err)
	}
	if tail != (appwire.JobOutputPage{}) {
		t.Errorf("unavailable output fabricated a success page: %+v", tail)
	}
}

func TestSessionJobOutputPageNilManager(t *testing.T) {
	t.Parallel()
	var s *Session
	if _, found, err := s.JobOutputPage("job_1", nil, 0); err != nil || found {
		t.Errorf("nil session JobOutputPage: found=%v err=%v", found, err)
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
	// Seed the running job directly rather than launch a real command: the
	// behavior under test is recordForRead's cloning, and a command that exits
	// could be finalized out of jm.running before the snapshot is taken.
	rec := &jobstore.JobRecord{
		JobID: "job_live", Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: "sess_1", StartedAt: time.Now(),
	}
	jm.running[rec.JobID] = &runningJob{rec: rec}

	_, got, err := jm.recordForRead(rec.JobID)
	if err != nil || got == nil {
		t.Fatalf("recordForRead: rec=%v err=%v", got, err)
	}
	if got == rec {
		t.Fatal("recordForRead returned the live record by pointer; its caller reads it after the lock is released")
	}
	if got.JobID != rec.JobID {
		t.Errorf("snapshot JobID = %q, want %q", got.JobID, rec.JobID)
	}
}
