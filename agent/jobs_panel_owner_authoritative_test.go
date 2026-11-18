package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

// TestLoadSessionJobGetPreferOwnerCompletedOverForwardedRunning is the
// owner-authoritative dedupe test for the COLD path. It builds the state the
// issue describes: a descendant's job events forwarded into an ancestor's
// journal (which stopped at the forwarded job_started, so the copy is a stale
// running row) and the owner's own journal holding the completed record with
// the end time and exit code. The cold read must return the owner's record, by
// the same rule the live recursive walk applies through
// keepIncomingDescendantRow.
//
// It asserts through LoadSessionJobGet — the exported cold read the hub's
// past-session fallback calls — so the same test compiles against the unfixed
// tree and fails there on BEHAVIOR, not on a missing symbol.
func TestLoadSessionJobGetPreferOwnerCompletedOverForwardedRunning(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	ended := started.Add(90 * time.Second)
	exit := 3

	// The ancestor's journal holds the FORWARDED copy: the owner's start event
	// as the ancestor saw it, with the forwarding session's visibility. No
	// terminal forward landed, so it folds to a running record.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	// The owner's journal holds the authoritative lifecycle: the completed
	// status, the end time, and the exit code the forwarded copy never got.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	// Read the job from the ANCESTOR's session: that is where a hub past-session
	// fallback lands when it is pointed at the wrong branch of the tree, and it
	// is where the stale copy lives.
	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's completed record")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q from the owner journal (the forwarded copy is stale running)",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.EndedAt == "" {
		t.Fatal("endedAt is empty, want the owner journal's end time")
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the owner journal", job.ExitCode, exit)
	}
	if job.OwnerSessionID != ownerSession {
		t.Fatalf("ownerSessionId = %q, want the owner %q", job.OwnerSessionID, ownerSession)
	}
}

// TestLoadSessionJobRecordDiagnostics_OwnerAvailableHasNoFallback pins the
// diagnostics on the same fixture: when the owner's record wins, the read must
// not also claim it fell back to a forwarded copy.
func TestLoadSessionJobRecordDiagnostics_OwnerAvailableHasNoFallback(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	rec, found, diagnostics, err := LoadSessionJobRecordWithDiagnostics(stateDir, rootSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobRecordWithDiagnostics: %v", err)
	}
	if !found || rec == nil {
		t.Fatal("found = false, want the owner's completed record")
	}
	if diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = true although the owner record was available")
	}
	if diagnostics.OwnerUnavailable {
		t.Fatal("OwnerUnavailable = true although the owner journal was readable")
	}
}

// TestLoadSessionJobRecordFallsBackToForwardedCopyWhenOwnerUnavailable pins the
// other half of the ruling the triage warns about: dropping forwarded copies
// outright would erase the only surviving evidence for a descendant whose own
// journal is gone. The forwarded record must still be returned — and must say
// it is a fallback.
func TestLoadSessionJobRecordFallsBackToForwardedCopyWhenOwnerUnavailable(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)
	// The owner session never materialized a journal at all.

	rec, found, diagnostics, err := LoadSessionJobRecordWithDiagnostics(stateDir, rootSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobRecordWithDiagnostics: %v", err)
	}
	if !found || rec == nil {
		t.Fatal("found = false, want the forwarded copy as a fallback")
	}
	if rec.OwnerSessionID != ownerSession {
		t.Fatalf("OwnerSessionID = %q, want the descendant owner %q", rec.OwnerSessionID, ownerSession)
	}
	if !diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = false, want true: the owner journal held no record")
	}
	if !diagnostics.OwnerUnavailable {
		t.Fatal("OwnerUnavailable = false, want true: the owner journal was absent")
	}
}

// TestLoadSessionJobRecordOwnerJournalTornTailIsReported covers the integrity
// diagnostic on the cold read itself: a torn trailing append in the journal the
// record came from must be visible to the caller, not silently swallowed.
func TestLoadSessionJobRecordOwnerJournalTornTailIsReported(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(jobID, sessionID, started)+"\n",
		`{"kind":"job_finished","seq":2,`,
	)

	rec, found, diagnostics, err := LoadSessionJobRecordWithDiagnostics(stateDir, sessionID, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobRecordWithDiagnostics: %v", err)
	}
	if !found || rec == nil {
		t.Fatal("found = false, want the record folded from the complete leading event")
	}
	if !diagnostics.TornTail {
		t.Fatal("TornTail = false, want true for an unterminated incomplete trailing event")
	}
}

// TestLoadSessionJobRecordOrphanFinishIsReported covers the lifecycle half on
// the cold read: a journal whose only event is a finish describes no whole job,
// and the read must say so through the typed diagnostic rather than presenting
// the best-effort record as complete. The error-free path is deliberate — a
// retained-history reader shows the row and flags it.
func TestLoadSessionJobRecordOrphanFinishIsReported(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	ended := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		`{"kind":"job_finished","seq":1,"job_id":"`+jobID+`","status":"completed","terminal_generation":"gen"}`+"\n",
	)

	rec, found, diagnostics, err := LoadSessionJobRecordWithDiagnostics(stateDir, sessionID, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobRecordWithDiagnostics must report damage, not fail: %v", err)
	}
	if !found || rec == nil {
		t.Fatal("found = false, want the best-effort record Fold reconstructs")
	}
	if !diagnostics.IncompleteLifecycle {
		t.Fatal("IncompleteLifecycle = false, want true for a finish with no start")
	}
	_ = ended
}

// TestFindLocalJobInProjectRejectsOwnerMismatch pins the owner-identity check
// the issue asks for on the path that already had one: a record whose
// OwnerSessionID contradicts its job ID is corruption, reported as an error
// rather than returned as that session's job.
func TestFindLocalJobInProjectRejectsOwnerMismatch(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	askedSession := identifier.MustNewSessionID()
	otherSession := identifier.MustNewSessionID()
	// The job ID encodes otherSession, so its true owner is otherSession.
	jobID := identifier.MustNewJobID(otherSession)
	started := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)

	// askedSession's journal holds an event for a job ID that encodes
	// otherSession, and the record itself also names otherSession. The lookup
	// resolves the owner FROM THE ID (that is what locateLocalJob passes as
	// ownerSessionID), so the coordinates check must reject the row rather
	// than adopting a foreign job into askedSession.
	writeJobJournalLines(t, jobsDir(stateDir, askedSession),
		jobStartedLine(jobID, otherSession, started)+"\n",
	)

	// Ask for the job as if it belonged to askedSession: the job ID says
	// otherwise, so the lookup must not adopt it.
	_, found, err := findLocalJobInProject(stateDir, askedSession, jobID)
	if err == nil {
		t.Fatal("findLocalJobInProject adopted a record whose owner contradicts its job ID")
	}
	if found {
		t.Fatal("found = true for a record the owner check rejected")
	}
	if errors.Is(err, jobstore.ErrIncompleteLifecycle) {
		t.Fatal("owner mismatch was reported as a lifecycle error, want the coordinates check's own error")
	}
}

// openJobJournal opens a session's jobs.jsonl for a fixture, creating the
// directory the store lives in.
func openJobJournal(t *testing.T, dir string) *jobstore.Store {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create job dir: %v", err)
	}
	store, err := jobstore.OpenNoSync(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open job journal: %v", err)
	}
	return store
}

func appendJobEvent(t *testing.T, store *jobstore.Store, event jobstore.Event) {
	t.Helper()
	if err := store.Append(event); err != nil {
		t.Fatalf("append job event: %v", err)
	}
}

func closeJobJournal(t *testing.T, store *jobstore.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close job journal: %v", err)
	}
}

// writeJobJournalLines writes raw journal lines to dir/jobs.jsonl, for
// fixtures that must control the exact bytes (a torn append, an orphan finish).
func writeJobJournalLines(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create job dir: %v", err)
	}
	content := strings.Join(lines, "")
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write job journal: %v", err)
	}
}

// jobStartedLine is the JSON a job's start lands in the journal as, so a raw
// fixture and a store-appended one describe the same event.
func jobStartedLine(jobID, ownerSession string, started time.Time) string {
	return `{"kind":"job_started","seq":1,"ts":"` + started.Format(time.RFC3339) +
		`","job_id":"` + jobID + `","type":"shell","owner_session_id":"` + ownerSession +
		`","visible_to_session_id":"` + ownerSession +
		`","started_at":"` + started.Format(time.RFC3339) + `"}`
}
