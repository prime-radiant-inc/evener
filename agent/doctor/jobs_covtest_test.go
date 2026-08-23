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
	legacyDelegateJobID = "job_legacydelegate_01"
	legacyWatchID       = "watch_legacy_01"
	delegateJobForWatch = "job_legacydelegate_02"
)

// richJobFixtureEvents produces a job that exercises every RenderJobs
// formatting branch: task, description, owner/visible, parent job, output path,
// and exhaustion metadata — fields the base jobsFixtureEvents fixture omits.
func richJobFixtureEvents() []jobstore.Event {
	started := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 31, 18, 2, 0, 0, time.UTC)
	exitOK := 0
	return []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_rich_01", Type: jobstore.JobShell,
			Command: "make all", Task: "build the thing", Description: "full build",
			OwnerSessionID: sidA, VisibleToSession: sidA, ParentJobID: "job_parent_01",
			StartedAt: &started, OutputPath: "/state/jobs/job_rich_01.log"},
		{Kind: jobstore.EventJobFinished, JobID: "job_rich_01", Status: jobstore.StatusCompleted,
			ExitCode: &exitOK, EndedAt: &ended, OutputBytes: 100, TerminalGen: "tg1",
			ExhaustionBudget: "max_turns", ExhaustionLimit: 500},
	}
}

// legacyDelegateJobEvents produces a delegate-type job record and a watch on
// that job, which legacyDelegateFailures must surface as state failures.
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
	// Write a jobs log with a rich job plus legacy delegate jobs.
	events := append(richJobFixtureEvents(), legacyDelegateJobEvents()...)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), events)
	// Write a delegate journal with one delegate owned by this session.
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_jobs_01", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: sid, VisibleSessionID: sid, ChildSessionID: "child_01",
			TranscriptRef: "proj:" + hash1 + ":child_01", Task: "do work", AgentType: "worker",
			ResolvedModel: "model-x", Resumable: true, ToolNameCeiling: []string{"communicate"},
		}}},
	})
	return base, sid
}

// TestJobs_RenderAllFormattingBranches exercises the RenderJobs branches for
// task, description, owner/visible, parent_job, output path, exhaustion
// metadata, and the delegate rendering block — all uncovered by the base
// fixture.
func TestJobs_RenderAllFormattingBranches(t *testing.T) {
	base, sid := jobsFixtureWithDelegate(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	out := RenderJobs(r)
	for _, want := range []string{
		"task=build the thing",
		"description=full build",
		"owner=" + sid,
		"visible=" + sid,
		"parent_job=job_parent_01",
		"output=/state/jobs/job_rich_01.log",
		"exhaustion: budget=max_turns  limit=500",
		"delegate dlg_jobs_01",
		"child=child_01",
		"model=model-x",
		"resumable=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
}

// TestJobs_LegacyDelegateFailures covers the legacyDelegateFailures path: a
// delegate-type job record and a watch on a delegate job are both surfaced as
// state failures.
func TestJobs_LegacyDelegateFailures(t *testing.T) {
	base, sid := jobsFixtureWithDelegate(t)
	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	out := RenderJobs(r)
	if !strings.Contains(out, "legacy_delegate_state") {
		t.Errorf("expected legacy_delegate_state failure; got:\n%s", out)
	}
	if !strings.Contains(out, legacyDelegateJobID) {
		t.Errorf("expected %s in failures; got:\n%s", legacyDelegateJobID, out)
	}
	if !strings.Contains(out, "legacy_delegate_watch_state") {
		t.Errorf("expected legacy_delegate_watch_state failure; got:\n%s", out)
	}
	if !strings.Contains(out, legacyWatchID) {
		t.Errorf("expected %s in failures; got:\n%s", legacyWatchID, out)
	}
}

// TestJobs_DelegateDiagnosticsTornTail covers the torn-tail diagnostic branch
// in stableDoctorDelegates: a truncated trailing batch in delegates.jsonl
// should be tolerated and reported as a diagnostic.
func TestJobs_DelegateDiagnosticsTornTail(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), nil)
	// Write a delegates.jsonl that has a valid created event then a torn trailing
	// partial line.
	store, err := delegatestore.Open(filepath.Join(bucket, "sessions", sid, "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendBatch(state, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_torn_01", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: sid, ChildSessionID: "child_torn", TranscriptRef: "proj:" + hash1 + ":child_torn",
			AgentType: "worker", Task: "torn tail test", ToolNameCeiling: []string{"communicate"},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Append a torn tail to the file so the reader sees an unterminated batch.
	delPath := filepath.Join(bucket, "sessions", sid, "delegates.jsonl")
	f, err := os.OpenFile(delPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":"delegate_created","delegate_id":"dlg_torn`)
	_ = f.Close()

	r, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	foundTornTail := false
	for _, d := range r.Diagnostics {
		if strings.Contains(d, "delegate_journal_torn_tail") {
			foundTornTail = true
		}
	}
	if !foundTornTail {
		t.Errorf("expected torn-tail diagnostic, got %v", r.Diagnostics)
	}
}

// TestJobs_JobTreeRootSessionIDRedirect covers the meta-redirect branch in
// stableDoctorDelegates: when a session's meta carries a JobTreeRootSessionID,
// the delegate journal is loaded from the root session's directory.
func TestJobs_JobTreeRootSessionIDRedirect(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	rootSID := "root_session_01"
	childSID := sidA
	writeSession(t, bucket, rootSID)
	writeSession(t, bucket, childSID)
	// The child session's meta points at the root.
	meta := schema.SessionMeta{ID: childSID, JobTreeRootSessionID: rootSID}
	if err := schema.SaveSessionMeta(bucket, meta); err != nil {
		t.Fatal(err)
	}
	// Jobs log on the child session.
	writeJobsEvents(t, filepath.Join(bucket, "sessions", childSID, "jobs.jsonl"), nil)
	// Delegates journal on the ROOT session — the redirect should find it.
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
	// The delegate should appear because the redirect loaded the root's journal.
	if len(r.Delegates) != 1 || r.Delegates[0].DelegateID != "dlg_redirect_01" {
		t.Errorf("expected 1 delegate via redirect, got %v", r.Delegates)
	}
}

// TestJobs_ReadEventsError covers the ReadEvents error path in Jobs: a corrupt
// (non-trailing) jobs.jsonl line should produce an error, not an empty report.
func TestJobs_ReadEventsError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	writeFile(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), "{not json}\n")
	_, err := Jobs(base, sidA, JobOpts{})
	if err == nil {
		t.Fatal("expected an error from a corrupt jobs.jsonl, got nil")
	}
}
