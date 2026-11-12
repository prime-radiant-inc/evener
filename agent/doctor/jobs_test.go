package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

const (
	completedJobID = "job_02wMz5TxvEMoJEDTDGOTi1"
	timeoutJobID   = "job_02wMz5TxvEMoJEDTDGOTi2"
	absentJobID    = "job_02wMz5TxvEMoJEDTDGOTi9"
)

var (
	jobStartedAt = time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	jobEndedAt   = time.Date(2026, 7, 31, 18, 2, 0, 0, time.UTC)
)

// jobsFixtureEvents records the two job shapes a diagnostician meets: a shell
// job that completed, a shell job the run timeout stopped with no output at all
// (the 2026-07-31 diagnosis shape) whose terminal notification never landed.
func jobsFixtureEvents() []jobstore.Event {
	exitOK, exitTimeout := 0, -1
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
	want := []string{timeoutJobID, completedJobID}
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

const (
	legacyDelegateJobID = "job_legacydelegate_01"
	legacyWatchID       = "watch_legacy_01"
	delegateJobForWatch = "job_legacydelegate_02"
)

// richJobFixtureEvents produces a job carrying every optional field RenderJobs
// prints: task, description, owner/visible, parent job, output path, and
// exhaustion metadata.
func richJobFixtureEvents() []jobstore.Event {
	started := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 31, 18, 2, 0, 0, time.UTC)
	exitOK := 0
	return []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_rich_01", Type: jobstore.JobShell,
			Command: "make all", Task: "build the thing", Description: "full build",
			OwnerSessionID: "owner_sentinel_session", VisibleToSession: "visible_sentinel_session", ParentJobID: "job_parent_01",
			StartedAt: &started, OutputPath: "/state/jobs/job_rich_01.log"},
		{Kind: jobstore.EventJobFinished, JobID: "job_rich_01", Status: jobstore.StatusCompleted,
			ExitCode: &exitOK, EndedAt: &ended, OutputBytes: 100, TerminalGen: "tg1",
			ExhaustionBudget: "max_turns", ExhaustionLimit: 500},
	}
}

// legacyDelegateJobEvents produces delegate-type job records and a watch on
// one of them, which the jobs report must surface as state failures.
func legacyDelegateJobEvents() []jobstore.Event {
	return []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: legacyDelegateJobID, Type: "delegate", OwnerSessionID: sidA},
		{Kind: jobstore.EventJobStarted, JobID: delegateJobForWatch, Type: "delegate", OwnerSessionID: sidA},
		{Kind: jobstore.EventWatchRegistered, WatchID: legacyWatchID,
			Watch: &jobstore.WatchEvent{Target: "job:" + delegateJobForWatch,
				OwnerSessionID: sidA, VisibleSessionID: sidA,
				Generation: "gen1", ConfigHash: "hash1"}},
	}
}

func jobsFixtureWithDelegate(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	// The job's owner and visibility name other sessions so the rendered
	// report carries them only through those fields.
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), append(richJobFixtureEvents(), legacyDelegateJobEvents()...))
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_jobs_01", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: sid, VisibleSessionID: sid, ChildSessionID: "child_01",
			TranscriptRef: "proj:" + hash1 + ":child_01", Task: "do work", AgentType: "worker",
			ResolvedModel: "model-x", Resumable: true, ToolNameCeiling: []string{"communicate"},
		}}},
	})
	return base, sid
}

// RenderJobs prints every optional field a job or delegate carries.
func TestJobs_RenderAllFormattingBranches(t *testing.T) {
	base, sid := jobsFixtureWithDelegate(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	out := RenderJobs(r)
	for _, want := range []string{
		"build the thing",
		"full build",
		"owner_sentinel_session",
		"visible_sentinel_session",
		"job_parent_01",
		"/state/jobs/job_rich_01.log",
		"max_turns",
		"500",
		"dlg_jobs_01",
		"child_01",
		"model-x",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
}

// A retired delegate-type job and a watch on one are reported as state
// failures naming them, and rendered with the report.
func TestJobs_LegacyDelegateFailures(t *testing.T) {
	base, sid := jobsFixtureWithDelegate(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Failures) != 2 || r.Failures[0].Code != "legacy_delegate_state" || r.Failures[1].Code != "legacy_delegate_watch_state" {
		t.Fatalf("failures = %+v, want the legacy job and watch failures", r.Failures)
	}
	out := RenderJobs(r)
	for _, want := range []string{legacyDelegateJobID, delegateJobForWatch, legacyWatchID} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered failures missing %q; got:\n%s", want, out)
		}
	}
}

// When a session's meta names a job-tree root, its delegates come from the
// root session's journal.
func TestJobs_JobTreeRootSessionIDRedirect(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	rootSID := "root_session_01"
	childSID := sidA
	writeSession(t, bucket, rootSID)
	writeSession(t, bucket, childSID)
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: childSID, JobTreeRootSessionID: rootSID}); err != nil {
		t.Fatal(err)
	}
	writeJobsEvents(t, filepath.Join(bucket, "sessions", childSID, "jobs.jsonl"), nil)
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", rootSID, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_redirect_01", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: childSID, ChildSessionID: "child_redirect", TranscriptRef: "proj:" + hash1 + ":child_redirect",
			AgentType: "worker", Task: "redirect test", ToolNameCeiling: []string{"communicate"},
		}}},
	})

	r, err := Jobs(base, childSID, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Delegates) != 1 || r.Delegates[0].DelegateID != "dlg_redirect_01" {
		t.Errorf("expected 1 delegate via redirect, got %v", r.Delegates)
	}
}

// A delegates journal ending in a torn, unterminated batch still loads, and
// the report carries a diagnostic for the ignored tail.
func TestJobs_DelegateDiagnosticsTornTail(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), nil)
	delPath := filepath.Join(bucket, "sessions", sidA, "delegates.jsonl")
	writeDelegateEvents(t, delPath, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_torn_01", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: sidA, ChildSessionID: "child_torn", TranscriptRef: "proj:" + hash1 + ":child_torn",
			AgentType: "worker", Task: "torn tail test", ToolNameCeiling: []string{"communicate"},
		}}},
	})
	f, err := os.OpenFile(delPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"delegate_created","delegate_id":"dlg_torn`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Jobs(base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(r.Delegates) != 1 || len(r.Diagnostics) != 1 {
		t.Errorf("delegates = %v, diagnostics = %v; want the stored delegate and one torn-tail diagnostic", r.Delegates, r.Diagnostics)
	}
}

// In doctor.Jobs, delegate records must normalize transcript_ref to match the
// canonical proj:<bucket>:<id> scheme (or empty when unlocatable/missing),
// never leaking local:<id> recorded in delegates.jsonl (issue #6179).
func TestJobs_DelegateChildRefMatchesRootProjScheme(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	rootSID := sidA
	childSID := "02wMz5Txv2enqVTitaig6F"
	missingSID := "01MISSINGCHILDSESSIONXXXXXX"

	writeSession(t, bucket, rootSID)
	writeSession(t, bucket, childSID)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", rootSID, "jobs.jsonl"), nil)

	writeDelegateEvents(t, filepath.Join(bucket, "sessions", rootSID, "delegates.jsonl"), []delegatestore.Event{
		{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: "dlg_1",
			Created: &delegatestore.DelegateCreated{
				Descriptor: delegatestore.Descriptor{
					OwnerSessionID:   rootSID,
					VisibleSessionID: rootSID,
					ChildSessionID:   childSID,
					TranscriptRef:    "local:" + childSID,
					AgentType:        "worker",
					Task:             "inspect child",
					Resumable:        true,
					ToolNameCeiling:  []string{"communicate"},
				},
			},
		},
		{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: "dlg_missing",
			Created: &delegatestore.DelegateCreated{
				Descriptor: delegatestore.Descriptor{
					OwnerSessionID:   rootSID,
					VisibleSessionID: rootSID,
					ChildSessionID:   missingSID,
					TranscriptRef:    "local:" + missingSID,
					AgentType:        "worker",
					Task:             "inspect missing child",
					Resumable:        true,
					ToolNameCeiling:  []string{"communicate"},
				},
			},
		},
	})

	report, err := Jobs(base, rootSID, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(report.Delegates) != 2 {
		t.Fatalf("expected 2 delegates, got %d", len(report.Delegates))
	}
	// Existing child must have canonical proj: ref
	wantChildRef := "proj:" + hash1 + ":" + childSID
	if got := report.Delegates[0].TranscriptRef; got != wantChildRef {
		t.Errorf("Delegates[0].TranscriptRef = %q, want %q", got, wantChildRef)
	}
	// Missing child must have empty ref, never local:
	if got := report.Delegates[1].TranscriptRef; got != "" {
		t.Errorf("Delegates[1].TranscriptRef = %q, want empty", got)
	}
}
