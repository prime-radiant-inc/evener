package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// forwardingChild returns a child job manager whose forwarded events land in
// the returned parent job manager's journal.
func forwardingChild(t *testing.T) (parent, child *jobManager) {
	t.Helper()
	parent = newTestJM(t)
	child = newTestJM(t)
	child.parentJobID = "parent-job"
	child.forward = parent.forwardEvent
	return parent, child
}

// finishChildJob appends a started+finished pair for a background shell job to
// the child's own journal.
func finishChildJob(t *testing.T, child *jobManager, jobID, gen string) {
	t.Helper()
	started := child.now()
	ended := child.now()
	code := 0
	for _, e := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, Command: "true", Background: true,
			OwnerSessionID: child.sessionID, VisibleToSession: child.sessionID, ParentJobID: "parent-job", StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, ExitCode: &code, EndedAt: &ended, TerminalGen: gen},
	} {
		if err := child.store.Append(e); err != nil {
			t.Fatalf("append %s for %s: %v", e.Kind, jobID, err)
		}
	}
}

func countParentEvents(t *testing.T, parent *jobManager, kind jobstore.EventKind, jobID, gen string) int {
	t.Helper()
	events, err := parent.store.LoadEvents()
	if err != nil {
		t.Fatalf("load parent events: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == kind && e.JobID == jobID && (gen == "" || e.TerminalGen == gen) {
			n++
		}
	}
	return n
}

func TestChildRestoreDoesNotReforwardDeliveredTerminalJobs(t *testing.T) {
	parent, child := forwardingChild(t)
	finishChildJob(t, child, "job-a", "gen-a")
	finishChildJob(t, child, "job-b", "gen-b")

	for range 3 {
		if err := child.recoverForwardedTerminalEvents(); err != nil {
			t.Fatalf("recover forwarded terminal events: %v", err)
		}
	}

	for jobID, gen := range map[string]string{"job-a": "gen-a", "job-b": "gen-b"} {
		if got := countParentEvents(t, parent, jobstore.EventJobStarted, jobID, ""); got != 1 {
			t.Errorf("%s: parent job_started count = %d, want 1", jobID, got)
		}
		if got := countParentEvents(t, parent, jobstore.EventJobFinished, jobID, gen); got != 1 {
			t.Errorf("%s: parent job_finished count = %d, want 1", jobID, got)
		}
	}
	recs, err := parent.store.Load()
	if err != nil {
		t.Fatalf("load parent records: %v", err)
	}
	if rec := recs["job-a"]; rec == nil || !rec.Background || rec.TerminalGen != "gen-a" {
		t.Fatalf("parent record for job-a = %+v, want background with terminal generation", rec)
	}
}

func TestChildRestoreForwardsSecondGenerationFromAnotherJournalOnce(t *testing.T) {
	parent, child := forwardingChild(t)
	finishChildJob(t, child, "job-a", "gen-1")
	if err := child.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("first recovery: %v", err)
	}

	// The child later settles the same job under a new generation.
	child2 := newTestJM(t)
	child2.parentJobID = "parent-job"
	child2.forward = parent.forwardEvent
	finishChildJob(t, child2, "job-a", "gen-2")
	for range 2 {
		if err := child2.recoverForwardedTerminalEvents(); err != nil {
			t.Fatalf("second recovery: %v", err)
		}
	}

	for _, gen := range []string{"gen-1", "gen-2"} {
		if got := countParentEvents(t, parent, jobstore.EventJobFinished, "job-a", gen); got != 1 {
			t.Errorf("parent job_finished count for %s = %d, want 1", gen, got)
		}
	}
}

func TestChildRestoreRecoversTerminalJobParentLacks(t *testing.T) {
	parent, child := forwardingChild(t)
	finishChildJob(t, child, "job-a", "gen-a")
	finishChildJob(t, child, "job-b", "gen-b")

	// The parent saw job-a start but crashed before it recorded the finish.
	started := child.now()
	if err := parent.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job-a", Type: jobstore.JobShell,
		OwnerSessionID: child.sessionID, ParentJobID: "parent-job", StartedAt: &started}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	if err := child.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("recover forwarded terminal events: %v", err)
	}

	for jobID, gen := range map[string]string{"job-a": "gen-a", "job-b": "gen-b"} {
		if got := countParentEvents(t, parent, jobstore.EventJobStarted, jobID, ""); got != 1 {
			t.Errorf("%s: parent job_started count = %d, want 1", jobID, got)
		}
		if got := countParentEvents(t, parent, jobstore.EventJobFinished, jobID, gen); got != 1 {
			t.Errorf("%s: parent job_finished count = %d, want 1", jobID, got)
		}
	}
}

func TestChildRestoreDoesNotReforwardPendingNotification(t *testing.T) {
	parent, child := forwardingChild(t)
	finishChildJob(t, child, "job-a", "gen-a")
	if err := child.store.Append(jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: child.now(), JobID: "job-a", TerminalGen: "gen-a"}); err != nil {
		t.Fatalf("append pending: %v", err)
	}

	for range 3 {
		if err := child.recoverForwardedTerminalEvents(); err != nil {
			t.Fatalf("recover terminal events: %v", err)
		}
		if err := child.recoverForwardedPendingNotifications(); err != nil {
			t.Fatalf("recover pending notifications: %v", err)
		}
	}

	if got := countParentEvents(t, parent, jobstore.EventJobNotificationPending, "job-a", "gen-a"); got != 1 {
		t.Fatalf("parent pending notification count = %d, want 1", got)
	}
}
