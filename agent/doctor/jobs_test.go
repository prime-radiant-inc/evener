package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	completedJobID = "job_02wMz5TxvEMoJEDTDGOTi1"
	timeoutJobID   = "job_02wMz5TxvEMoJEDTDGOTi2"
	delegateJobID  = "job_02wMz5TxvEMoJEDTDGOTi3"
	absentJobID    = "job_02wMz5TxvEMoJEDTDGOTi9"
)

var (
	jobStartedAt = time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	jobEndedAt   = time.Date(2026, 7, 31, 18, 2, 0, 0, time.UTC)
)

// jobsFixtureEvents records the three job shapes a diagnostician meets: a shell
// job that completed, a shell job the run timeout stopped with no output at all
// (the 2026-07-31 diagnosis shape) whose terminal notification never landed, and
// a delegate job still running.
func jobsFixtureEvents() []jobstore.Event {
	exitOK, exitTimeout := 0, -1
	resumable := true
	// The timeout job is appended FIRST although its id sorts after the completed
	// job's, so the expected order can only come from the log's append order — an
	// id sort would produce a different list.
	return []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: timeoutJobID, Type: jobstore.JobShell, Command: "npm run dev",
			Description: "dev server", OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobFinished, JobID: timeoutJobID, Status: jobstore.StatusStopped, Reason: "run_timeout",
			ExitCode: &exitTimeout, EndedAt: &jobEndedAt, OutputBytes: 0, TerminalGen: "tg2"},
		{Kind: jobstore.EventJobNotificationPending, JobID: timeoutJobID, TerminalGen: "tg2"},

		{Kind: jobstore.EventJobStarted, JobID: completedJobID, Type: jobstore.JobShell, Command: "make test",
			OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt,
			OutputPath: "/state/jobs/" + completedJobID + ".log"},
		{Kind: jobstore.EventJobFinished, JobID: completedJobID, Status: jobstore.StatusCompleted,
			ExitCode: &exitOK, EndedAt: &jobEndedAt, OutputBytes: 4096, TerminalGen: "tg1"},

		{Kind: jobstore.EventJobStarted, JobID: delegateJobID, Type: jobstore.JobDelegate, Task: "review the diff",
			DelegateID: "dlg_02wMz5TxvEMoJEDTDGOTi4", ParentJobID: completedJobID,
			OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobSessionAssigned, JobID: delegateJobID, TranscriptRef: "local:" + sidB, Resumable: &resumable},
	}
}

func jobsFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), jobsFixtureEvents())
	return base, sid
}

func findJob(r JobReport, jobID string) *JobView {
	for i := range r.Jobs {
		if r.Jobs[i].JobID == jobID {
			return &r.Jobs[i]
		}
	}
	return nil
}

// The list answers "what jobs has this session run", in the durable append order
// the log defines — not a map order that shuffles between runs.
func TestJobs_ListsFoldedRecordsInAppendOrder(t *testing.T) {
	base, sid := jobsFixture(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if r.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", r.SessionID, sid)
	}
	got := make([]string, 0, len(r.Jobs))
	for _, j := range r.Jobs {
		got = append(got, j.JobID)
	}
	want := []string{timeoutJobID, completedJobID, delegateJobID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("job order = %v, want %v", got, want)
	}
}

// "What state is job X in": the terminal facts the live /status endpoint was the
// only route to — status, reason, exit code, output bytes, and both timestamps.
func TestJobs_TerminalStateOfOneJob(t *testing.T) {
	base, sid := jobsFixture(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	j := findJob(r, timeoutJobID)
	if j == nil {
		t.Fatalf("%s missing from report", timeoutJobID)
	}
	if j.Status != "stopped" || j.Reason != "run_timeout" {
		t.Errorf("status/reason = %q/%q, want stopped/run_timeout", j.Status, j.Reason)
	}
	if j.ExitCode == nil || *j.ExitCode != -1 {
		t.Errorf("ExitCode = %v, want -1", j.ExitCode)
	}
	if j.OutputBytes != 0 {
		t.Errorf("OutputBytes = %d, want 0", j.OutputBytes)
	}
	if !j.StartedAt.Equal(jobStartedAt) {
		t.Errorf("StartedAt = %v, want %v", j.StartedAt, jobStartedAt)
	}
	if j.EndedAt == nil || !j.EndedAt.Equal(jobEndedAt) {
		t.Errorf("EndedAt = %v, want %v", j.EndedAt, jobEndedAt)
	}
	if j.Type != "shell" || j.Command != "npm run dev" {
		t.Errorf("type/command = %q/%q, want shell/npm run dev", j.Type, j.Command)
	}
	// The caller was never told this job died: the notification is still pending.
	if j.NotifyState != "pending" {
		t.Errorf("NotifyState = %q, want pending", j.NotifyState)
	}
}

// A delegate job carries the links a diagnostician pivots on: the child
// transcript ref (serf-doctor transcript <ref>), its delegate id, and its parent job.
func TestJobs_DelegateJobCarriesPivotLinks(t *testing.T) {
	base, sid := jobsFixture(t)
	r, _ := Jobs(base, sid, JobOpts{})
	j := findJob(r, delegateJobID)
	if j == nil {
		t.Fatalf("%s missing from report", delegateJobID)
	}
	if j.Status != "running" {
		t.Errorf("Status = %q, want running", j.Status)
	}
	if j.TranscriptRef != "local:"+sidB {
		t.Errorf("TranscriptRef = %q, want local:%s", j.TranscriptRef, sidB)
	}
	if j.DelegateID != "dlg_02wMz5TxvEMoJEDTDGOTi4" {
		t.Errorf("DelegateID = %q", j.DelegateID)
	}
	if j.ParentJobID != completedJobID {
		t.Errorf("ParentJobID = %q, want %q", j.ParentJobID, completedJobID)
	}
	if j.Task != "review the diff" {
		t.Errorf("Task = %q, want the delegate task", j.Task)
	}
}

func TestJobs_JobIDFilter(t *testing.T) {
	base, sid := jobsFixture(t)
	r, err := Jobs(base, sid, JobOpts{JobID: timeoutJobID})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Jobs) != 1 || r.Jobs[0].JobID != timeoutJobID {
		t.Fatalf("--job filter should yield only %s, got %d jobs", timeoutJobID, len(r.Jobs))
	}
	if r.Filtered != "job:"+timeoutJobID {
		t.Errorf("Filtered = %q, want job:%s", r.Filtered, timeoutJobID)
	}
}

// A session that never ran a job has no jobs.jsonl at all. That is a clean
// answer ("none"), never an error — the same way watches reads the same file.
func TestJobs_MissingJobsFileIsCleanAnswer(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	if err := os.Remove(filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")); err != nil {
		t.Fatal(err)
	}

	r, err := Jobs(base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs on a session that never ran jobs: %v", err)
	}
	if len(r.Jobs) != 0 {
		t.Errorf("Jobs = %d, want 0", len(r.Jobs))
	}
	if out := RenderJobs(r); !strings.Contains(out, "no jobs recorded") {
		t.Errorf("render should say no jobs recorded; got:\n%s", out)
	}
}

func TestJobs_EmptyJobsFileIsCleanAnswer(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)

	r, err := Jobs(base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs on an empty jobs.jsonl: %v", err)
	}
	if len(r.Jobs) != 0 {
		t.Errorf("Jobs = %d, want 0", len(r.Jobs))
	}
}

// Malformed lines get exactly the treatment watches gives the same file: an
// unparsable trailing line is a tolerated in-flight append, an earlier one is
// reported corruption. One reader, one rule.
func TestJobs_MalformedLineHandlingMatchesWatches(t *testing.T) {
	valid := `{"kind":"job_started","seq":1,"job_id":"` + completedJobID + `","type":"shell","command":"make test"}`

	t.Run("trailing partial line tolerated", func(t *testing.T) {
		base := t.TempDir()
		bucket := stateHomeBucket(base, hash1)
		writeSession(t, bucket, sidA)
		writeFile(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), valid+"\n{\"kind\":\"job_fin")

		r, err := Jobs(base, sidA, JobOpts{})
		_, watchErr := Watches(base, sidA, WatchOpts{})
		if (err == nil) != (watchErr == nil) {
			t.Fatalf("jobs err = %v but watches err = %v; the two readers must agree", err, watchErr)
		}
		if err != nil {
			t.Fatalf("trailing partial line should be tolerated: %v", err)
		}
		if len(r.Jobs) != 1 {
			t.Errorf("Jobs = %d, want the one complete record", len(r.Jobs))
		}
	})

	t.Run("earlier malformed line is corruption", func(t *testing.T) {
		base := t.TempDir()
		bucket := stateHomeBucket(base, hash1)
		writeSession(t, bucket, sidA)
		writeFile(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), "{not json}\n"+valid+"\n")

		_, err := Jobs(base, sidA, JobOpts{})
		_, watchErr := Watches(base, sidA, WatchOpts{})
		if (err == nil) != (watchErr == nil) {
			t.Fatalf("jobs err = %v but watches err = %v; the two readers must agree", err, watchErr)
		}
		if err == nil {
			t.Fatal("a malformed non-trailing line should be reported, not silently dropped")
		}
	})
}

func TestJobs_RenderTellsTheTerminalStory(t *testing.T) {
	base, sid := jobsFixture(t)
	r, _ := Jobs(base, sid, JobOpts{})
	out := RenderJobs(r)

	for _, want := range []string{
		"job " + timeoutJobID + "  (stopped: run_timeout)",
		"exit=-1",
		"output_bytes=0",
		"started=2026-07-31T18:00:00Z",
		"ended=2026-07-31T18:02:00Z",
		"command=npm run dev",
		"notify=pending",
		"transcript=local:" + sidB,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
}

func TestEmptyJobsMessage(t *testing.T) {
	cases := map[string]string{
		"":                   "no jobs recorded",
		"job:" + absentJobID: "job " + absentJobID + " not found in this session",
	}
	for filtered, want := range cases {
		if got := emptyJobsMessage(filtered); got != want {
			t.Errorf("emptyJobsMessage(%q) = %q, want %q", filtered, got, want)
		}
	}
}

// A --job filter that matches nothing must not read as "this session ran no
// jobs" — it ran jobs, just not that one.
func TestJobs_UnknownJobIDMessageIsUnambiguous(t *testing.T) {
	base, sid := jobsFixture(t)
	r, err := Jobs(base, sid, JobOpts{JobID: absentJobID})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderJobs(r)
	if strings.Contains(out, "no jobs recorded") {
		t.Errorf("an unmatched --job filter should not say 'no jobs recorded':\n%s", out)
	}
	if !strings.Contains(out, "job "+absentJobID+" not found in this session") {
		t.Errorf("expected the not-found message:\n%s", out)
	}
}
