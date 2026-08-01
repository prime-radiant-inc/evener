package agent

import (
	"encoding/json"
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

// HasOutput means "a tail read is worth attempting", and either signal on
// its own is reason enough: bytes were counted for a job whose OutputPath
// the record never carried, or a path was recorded for a job that has not
// written to it yet.
func TestSummarizeJobRecordHasOutputEitherSignal(t *testing.T) {
	bytesOnly := summarizeJobRecord(&jobstore.JobRecord{JobID: "job_b", OutputBytes: 42})
	if !bytesOnly.HasOutput {
		t.Error("HasOutput should be true when bytes were counted and no OutputPath is recorded")
	}
	pathOnly := summarizeJobRecord(&jobstore.JobRecord{JobID: "job_p", OutputPath: "/tmp/out.log"})
	if !pathOnly.HasOutput {
		t.Error("HasOutput should be true when an OutputPath is recorded and no bytes were counted")
	}
	if neither := summarizeJobRecord(&jobstore.JobRecord{JobID: "job_n"}); neither.HasOutput {
		t.Error("HasOutput should be false with neither an output path nor counted bytes")
	}
}

// Zero jobs must reach the wire as [], never null. jobData.ts reads null as
// "this daemon has no job list at all" (an old one with no jobsFn) and
// renders a capability gap; [] is the honest "no jobs ran", which the panel
// shows as "No jobs yet". Every producer of this payload owes the same [].
func TestJobSummariesEmptySetMarshalsAsJSONArray(t *testing.T) {
	assertEmptyJSONArray := func(what string, jobs []JobSummary) {
		t.Helper()
		encoded, err := json.Marshal(jobs)
		if err != nil {
			t.Fatalf("%s: marshal: %v", what, err)
		}
		if string(encoded) != "[]" {
			t.Errorf("%s: got %s, want []", what, encoded)
		}
	}
	assertEmptyJSONArray("summarizeJobRecords(nil)", summarizeJobRecords(nil))

	jm := newTestJM(t)
	live, err := (&Session{id: jm.sessionID, jobManager: jm}).JobSummaries()
	if err != nil {
		t.Fatalf("live JobSummaries: %v", err)
	}
	assertEmptyJSONArray("a live session that ran no jobs", live)

	var nilSession *Session
	none, err := nilSession.JobSummaries()
	if err != nil {
		t.Fatalf("nil-manager JobSummaries: %v", err)
	}
	assertEmptyJSONArray("a session with no job manager", none)
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
	// Non-nil too: the hub's past-session fallback puts this straight into
	// JobsListResponse.Data, where nil would marshal as null and read as a
	// capability gap instead of "no jobs ran".
	if encoded, err := json.Marshal(got); err != nil || string(encoded) != "[]" {
		t.Errorf("no-log job list: got %s (err=%v), want []", encoded, err)
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
	st, err := jobstore.Open(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
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
	got, err := s.JobSummaries()
	if err != nil {
		t.Fatalf("nil session JobSummaries: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("nil session JobSummaries: %+v", got)
	}
	if _, found, err := s.JobOutputTail("job_1", 0); err != nil || found {
		t.Errorf("nil session JobOutputTail: found=%v err=%v", found, err)
	}
}

// The live-daemon path end to end - the one cmd/serf/serve.go's SetJobsFunc
// actually calls. Every other test here reaches the projection directly or
// through the past-session reader; this one lets a real Session's own job
// manager create, feed and finish a job, then reads back what the durable
// store holds.
func TestSessionJobSummariesLiveSession(t *testing.T) {
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "make build", Description: "build the tree"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	appendManualJobOutput(s.jobManager, rec.JobID, "build ok\n")
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)

	got, err := s.JobSummaries()
	if err != nil {
		t.Fatalf("JobSummaries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("live job list: got %d jobs, want 1: %+v", len(got), got)
	}
	sum := got[0]
	if sum.JobID != rec.JobID || sum.Type != "shell" || sum.Status != "completed" {
		t.Errorf("identity/status: %+v", sum)
	}
	if sum.Description != "build the tree" || sum.Command != "make build" {
		t.Errorf("description/command: %+v", sum)
	}
	if sum.StartedAt == "" || sum.EndedAt == "" {
		t.Errorf("a finished job carries both timestamps: %+v", sum)
	}
	if !sum.HasOutput || sum.OutputBytes != int64(len("build ok\n")) {
		t.Errorf("output: %+v", sum)
	}
}

// A RUNNING job's row must report the live record, not the fold of the
// durable log alone. Background is live-only (jobstore.JobRecord.Background
// is json:"-": no event carries it), so a listing built from the store alone
// answers false for every job however it was launched; OutputBytes is only
// stamped durably at terminal, so a running job's folded row counts 0 bytes
// however much it has written. Both are what jobManager.listWithError's
// live overlay exists to correct, and this payload owes the same answer.
func TestSessionJobSummariesOverlaysLiveRunningJob(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{id: jm.sessionID, jobManager: jm}
	run, err := jm.newDelayedShell(shellArgs{Command: "tail -f server.log", Background: true})
	if err != nil {
		t.Fatalf("newDelayedShell: %v", err)
	}
	if err := jm.commitDelayedShell(run); err != nil {
		t.Fatalf("commitDelayedShell: %v", err)
	}
	appendManualJobOutput(jm, run.rec.JobID, "listening\n")

	got, err := s.JobSummaries()
	if err != nil {
		t.Fatalf("JobSummaries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("job list: got %d jobs, want 1: %+v", len(got), got)
	}
	sum := got[0]
	if sum.JobID != run.rec.JobID || sum.Status != "running" {
		t.Fatalf("identity/status: %+v", sum)
	}
	if !sum.Background {
		t.Errorf("a live background shell must list background:true, got %+v", sum)
	}
	if sum.OutputBytes != int64(len("listening\n")) {
		t.Errorf("a running job lists its live output count: got %d, want %d (%+v)",
			sum.OutputBytes, len("listening\n"), sum)
	}
}

// A jobstore that cannot be read is not a session that ran no jobs. The
// past-session reader for this same payload (LoadSessionJobList) has always
// surfaced the read error; the live one used to answer the empty list a
// job-less session answers, which the webui renders as "No jobs yet" - a far
// more reassuring claim than the truth.
func TestSessionJobSummariesSurfacesLoadError(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{id: jm.sessionID, jobManager: jm}
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))

	got, err := s.JobSummaries()
	if err == nil {
		t.Fatalf("corrupt jobstore should surface an error, got %d jobs", len(got))
	}
	if got != nil {
		t.Errorf("a failed projection should carry no jobs, got %+v", got)
	}
}
