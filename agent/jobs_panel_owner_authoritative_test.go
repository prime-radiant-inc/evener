package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
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

// TestLoadsessionJobRecordDiagnostics_OwnerAvailableHasNoFallback pins the
// diagnostics on the same fixture: when the owner's record wins, the read must
// not also claim it fell back to a forwarded copy.
func TestLoadsessionJobRecordDiagnostics_OwnerAvailableHasNoFallback(t *testing.T) {
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the owner's completed record")
	}
	if read.diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = true although the owner record was available")
	}
	if read.diagnostics.OwnerUnavailable {
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the forwarded copy as a fallback")
	}
	if read.record.OwnerSessionID != ownerSession {
		t.Fatalf("OwnerSessionID = %q, want the descendant owner %q", read.record.OwnerSessionID, ownerSession)
	}
	if !read.diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = false, want true: the owner journal held no record")
	}
	if !read.diagnostics.OwnerUnavailable {
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

	read, err := loadSessionJobRecord(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the record folded from the complete leading event")
	}
	if !read.diagnostics.TornTail {
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

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		`{"kind":"job_finished","seq":1,"job_id":"`+jobID+`","status":"completed","terminal_generation":"gen"}`+"\n",
	)

	read, err := loadSessionJobRecord(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord must report damage, not fail: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the best-effort record Fold reconstructs")
	}
	if !read.diagnostics.IncompleteLifecycle {
		t.Fatal("IncompleteLifecycle = false, want true for a finish with no start")
	}
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

// TestLoadSessionJobRecordContradictoryAncestorRowIsNotAuthoritative is the
// medium-1 regression. A stale ancestor row that declares the ANCESTOR as its
// owner, while the job ID encodes the descendant, is not authoritative however
// confidently it names an owner: authority comes from the identifier, not from
// what the row claims. Such a row must never outrank the matching owner's
// completed record.
func TestLoadSessionJobRecordContradictoryAncestorRowIsNotAuthoritative(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exit := 0

	// The ancestor's journal holds a running row claiming ROOT as its owner. The
	// job ID says otherwise, so this row is a contradiction, not an authority.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: rootSession, VisibleToSession: rootSession,
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
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the owner's completed record")
	}
	if read.record.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %q, want %q: a row claiming the ancestor cannot outrank the ID's owner",
			read.record.Status, jobstore.StatusCompleted)
	}
	if read.record.OwnerSessionID != ownerSession {
		t.Fatalf("OwnerSessionID = %q, want the ID's owner %q", read.record.OwnerSessionID, ownerSession)
	}
	if read.diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = true although the authoritative owner record was available")
	}
}

// TestLoadSessionJobRecordKeepsOwnerDiagnosticWhenOwnerWins is the low-3
// regression. A healthy forwarded start sits in the ancestor's journal while the
// owner's journal holds an orphan finish: the owner's record is the one returned
// (it is the ID's owner, so it is authoritative), and the owner's integrity
// signal must be reported with it. The old merge returned the owner's damaged
// row from its own journal carrying no damage at all.
func TestLoadSessionJobRecordKeepsOwnerDiagnosticWhenOwnerWins(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 15, 30, 0, 0, time.UTC)

	// A healthy forwarded start in the ancestor's journal, carrying the owner's
	// own ID exactly as forwardEvent leaves it.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	// The owner's journal holds an ORPHAN finish: a terminal event with no start.
	// Its lifecycle is damaged, and the read must say so even though the damaged
	// record is not the one returned.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_finished","seq":1,"job_id":"`+jobID+`","status":"completed","terminal_generation":"gen"}`+"\n",
	)

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the forwarded copy as the surviving evidence")
	}
	// The owner's record won, so this is not a fallback — but its damage still
	// has to be reported, and it must come from the owner's journal.
	if read.sourceSessionID != ownerSession {
		t.Fatalf("sourceSessionID = %q, want the owner %q", read.sourceSessionID, ownerSession)
	}
	if read.diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = true although the returned record is the owner's own")
	}
	if !read.diagnostics.IncompleteLifecycle {
		t.Fatal("IncompleteLifecycle = false, want the owner journal's orphan finish reported")
	}
}

// TestLoadSessionJobRecordKeepsLocalDamageWhenOwnerWins covers the other
// direction of the same merge: the returned record is the owner's, and the
// FORWARDED journal is the damaged one. Its damage must still reach the caller —
// either journal being torn or incoherent is something a retained-history read
// has to say, and dropping it because the healthy branch won would hide it.
func TestLoadSessionJobRecordKeepsLocalDamageWhenOwnerWins(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 15, 45, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// The ancestor's journal ends in an unterminated append: damaged, but not
	// enough to hide the forwarded start that precedes it.
	writeJobJournalLines(t, jobsDir(stateDir, rootSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,`,
	)

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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the owner's completed record")
	}
	if read.sourceSessionID != ownerSession {
		t.Fatalf("sourceSessionID = %q, want the owner %q", read.sourceSessionID, ownerSession)
	}
	if !read.diagnostics.TornTail {
		t.Fatal("TornTail = false, want the forwarded journal's torn tail reported with the owner's record")
	}
}

// TestLoadSessionJobGetRoutesLegacyEmptyOwnerToItsJournal is the medium-2
// regression. A retained legacy start carries no OwnerSessionID, so the owner
// ref would be empty — a ref no client can resolve — while the command, terminal
// status and log all live beside the journal that supplied the record. The read
// must route to that journal.
func TestLoadSessionJobGetRoutesLegacyEmptyOwnerToItsJournal(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC)

	// No owner_session_id at all: the pre-owner journal shape.
	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","visible_to_session_id":"`+sessionID+
			`","started_at":"`+started.Format(time.RFC3339)+`"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the legacy record")
	}
	if job.OwnerRef == "" {
		t.Fatal("OwnerRef is empty: a legacy record must route to the journal that supplied it")
	}
	want := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	if job.OwnerRef != want {
		t.Fatalf("OwnerRef = %q, want %q (the session whose journal supplied the record)", job.OwnerRef, want)
	}
}

// TestLoadSessionJobGetRequiresRequestedScope is the medium-3 regression. The
// requested session holds no record of the job at all: the request cannot be
// answered from another session's journal just because the job ID names one.
// Scope is proven before data is served.
func TestLoadSessionJobGetRequiresRequestedScope(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	askedSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 17, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// Only the owner has a journal, and it has a complete lifecycle with a
	// command and an output path.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		Command: "rm -rf /important", StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	// The asked session is a real session with its own empty journal, so this is
	// not merely an absent session: it has no claim on this job.
	writeJobJournalLines(t, jobsDir(stateDir, askedSession), "")

	job, found, err := LoadSessionJobGet(stateDir, askedSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if found {
		t.Fatalf("found = true (status %q, command %q): the asked session holds no record of this job",
			job.Status, job.Command)
	}
}

// TestLoadSessionJobGetHonorsOwnerTombstone is the other half of medium 3: the
// job IS in the requested session's scope — the forwarded copy is right there —
// but the owner's deletion fence stands. A deletion that failed after writing
// the tombstone leaves the owner's work directory behind, so the marker is the
// evidence, and neither the owner's journal nor the forwarded copy may be
// served.
func TestLoadSessionJobGetHonorsOwnerTombstone(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)

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
	closeJobJournal(t, ownerStore)

	if err := schema.TombstoneSessionMeta(stateDir, ownerSession); err != nil {
		t.Fatalf("tombstone owner session: %v", err)
	}

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if found {
		t.Fatalf("found = true (status %q): the owner is fenced for deletion, so its job must not be served",
			job.Status)
	}
}

// TestLoadSessionJobGetEchoesRequestedRef pins the ref echo.
// The activity page opens a forwarded job under the ANCESTOR's ref, and the web
// job log rejects metadata whose ownerRef differs from the ref it requested —
// so returning the descendant's ref there threw away the command and terminal
// status the read had just recovered. The response must be verifiable against
// the requested scope, with the owner's identity riding OwnerSessionID.
func TestLoadSessionJobGetEchoesRequestedRef(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exit := 0

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		Command: "make test", StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		Command: "make test", StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's completed record")
	}
	wantRef := appwire.Ref{SourceID: "local", ThreadID: rootSession}.String()
	if job.OwnerRef != wantRef {
		t.Fatalf("OwnerRef = %q, want the requested ref %q: the client rejects a mismatch", job.OwnerRef, wantRef)
	}
	// The owner's identity is not lost: it rides the field that means it.
	if job.OwnerSessionID != ownerSession {
		t.Fatalf("OwnerSessionID = %q, want the owner %q", job.OwnerSessionID, ownerSession)
	}
	// And the payload the mismatch used to discard is present.
	if !job.Terminal || job.Command != "make test" {
		t.Fatalf("terminal = %v, command = %q: want the owner's terminal state and command", job.Terminal, job.Command)
	}
}

// TestLoadSessionJobGetUsesDamagedPrefixAsScope pins the damaged-prefix rule.
// An ancestor journal holding a valid forwarded start followed by a
// malformed durable line used to discard the decoded prefix and fail the whole
// read, making the descendant's intact record and output unreachable through the
// ancestor even though the start proved scope. The prefix is scope evidence; the
// error is preserved only when it settles nothing.
func TestLoadSessionJobGetUsesDamagedPrefixAsScope(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 19, 30, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// The ancestor's journal: a valid forwarded start, then a newline-terminated
	// record that is durably corrupt.
	writeJobJournalLines(t, jobsDir(stateDir, rootSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		"{this is not json}\n",
	)

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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet must use the decoded prefix as scope, got error: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's completed record")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q from the owner journal", job.Status, jobstore.StatusCompleted)
	}
}

// TestLoadSessionJobGetResolvesLegacyIDByOwnerHint pins the legacy hint path.
// A job saved before owner-encoded IDs has a bare UUID job ID and a
// populated owner field. The encoded owner is empty, so that field is the only
// pointer to the journal that may hold the finish — and it must be corroborated
// there before being believed, or a forwarded start keeps showing running.
func TestLoadSessionJobGetResolvesLegacyIDByOwnerHint(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	// A legacy shape: no encoded owner, so validateJobID rejects it and
	// encodedJobOwner returns "".
	jobID := "job_legacy_e2f3c4d5"
	started := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exit := 0

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	// The hint is corroborated here: the owner's journal holds the whole job.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner journal's completed record")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q: the corroborated owner hint must beat the forwarded start",
			job.Status, jobstore.StatusCompleted)
	}
}

// TestLoadSessionJobGetRejectsUncorroboratedLegacyHint is the other half of low
// 4: an owner field that names a session with no record of the job is not
// evidence. The scoped copy then stands — flagged as a fallback, never silently
// presented as the owner's.
func TestLoadSessionJobGetRejectsUncorroboratedLegacyHint(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := "job_legacy_a1b2c3d4"
	started := time.Date(2026, 11, 18, 20, 30, 0, 0, time.UTC)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)
	// The named owner never materialized a journal.

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the scoped copy as the surviving evidence")
	}
	if !read.diagnostics.ForwardedFallback {
		t.Fatal("ForwardedFallback = false, want true: the hint was not corroborated")
	}
	if !read.diagnostics.OwnerUnavailable {
		t.Fatal("OwnerUnavailable = false, want true: the hinted journal was absent")
	}
}

// TestLoadSessionJobGetSurfacesDiagnostics pins the diagnostic surface: a
// retained read's fallback and integrity facts must reach a production caller,
// not die in an internal struct both callers discard.
func TestLoadSessionJobGetSurfacesDiagnostics(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)
	// The owner's journal is gone, so this row is a forward-only fallback.

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded copy")
	}
	if !slices.Contains(job.Diagnostics, "forwarded_fallback") {
		t.Fatalf("Diagnostics = %v, want forwarded_fallback: a stale forwarded running row must be distinguishable from a live one",
			job.Diagnostics)
	}
	if !slices.Contains(job.Diagnostics, "owner_unavailable") {
		t.Fatalf("Diagnostics = %v, want owner_unavailable", job.Diagnostics)
	}
}

// TestLoadSessionJobGetReportsScanDamageNotHealthyRunning pins the scan-damage
// rule. The owner's journal holds a valid start followed by a
// newline-terminated line that cannot be decoded — so the scan returns the
// decoded start AND a parse error, and the finish may be the very line that was
// lost. The read used to drop the error the moment the prefix held a matching
// row and report an apparently healthy running job that never reached its
// finish. It must report what it could not establish instead.
func TestLoadSessionJobGetReportsScanDamageNotHealthyRunning(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(jobID, sessionID, started)+"\n",
		"{this is not json}\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet must still serve the surviving prefix, got error: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the best-effort record from the decoded prefix")
	}
	if !slices.Contains(job.Diagnostics, "journal_scan_incomplete") {
		t.Fatalf("Diagnostics = %v, want journal_scan_incomplete: the journal stopped at durable corruption, so the running row is not a settled state",
			job.Diagnostics)
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedOwnerPrefix is the other
// half of medium 3, and the half a naive "return the error" fix would get wrong.
// The owner's journal holds a start and then durable corruption, so its decoded
// prefix cannot say whether the job finished. The ancestor's forwarded copy is
// complete and terminal: it observed the finish. The damaged owner prefix must
// not displace that settled answer — an unreadable journal past the start is not
// evidence that the job is still running.
func TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedOwnerPrefix(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 22, 30, 0, 0, time.UTC)
	ended := started.Add(2 * time.Minute)
	exit := 2

	// The owner's journal: a valid start, then a durably corrupt line. The
	// finish it may have held is unreachable.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		"{this is not json}\n",
	)

	// The ancestor's journal holds the complete observed lifecycle.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_forwarded",
	})
	closeJobJournal(t, rootStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded terminal observation")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: an unreadable owner tail cannot overrule an observed finish",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the forwarded finish", job.ExitCode, exit)
	}
}

// TestLoadSessionJobOutputPageRoutesByValidatedOwner pins owner routing.
// The job ID encodes session A, but the retained start claims
// session B. With no explicit OutputPath the log lives under A's job directory,
// because A is the session that ran it and the validated identifier is what says
// so. Routing by the record's claim looked in B's directory and lost a readable
// log.
func TestLoadSessionJobOutputPageRoutesByValidatedOwner(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ownerSession := identifier.MustNewSessionID()
	claimingSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC)

	// The owner's journal: a start whose owner claim CONTRADICTS the job ID.
	// No OutputPath, so the reader must derive the default log location.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","status":"running","owner_session_id":"`+claimingSession+
			`","visible_to_session_id":"`+ownerSession+`","started_at":"`+started.Format(time.RFC3339)+`"}`+"\n",
	)

	// The log sits where the session that ran the job writes it.
	logPath := filepath.Join(jobsDir(stateDir, ownerSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("owner log"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A same-named log under the claiming session belongs to a different job, so a
	// read routed there would return another job's bytes.
	decoyPath := filepath.Join(jobsDir(stateDir, claimingSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(decoyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoyPath, []byte("decoy log"), 0o644); err != nil {
		t.Fatal(err)
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, ownerSession, jobID, nil, 64, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's log")
	}
	if page.Data != "owner log" {
		t.Fatalf("data = %q, want %q: the default log path follows the validated owner, not the record's claim",
			page.Data, "owner log")
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedLegacyOwner is the
// legacy half of the damaged-prefix rule. The job has a bare-UUID ID and its
// row claims an owner; that journal corroborates
// with a valid start followed by durable corruption. Corroboration proves the
// journal owns the job, not that the job finished — so an observed finish in the
// scoped copy must not be replaced by a running row from a prefix that cannot
// reach it.
func TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedLegacyOwner(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := "job_legacy_d4d5e6f7"
	started := time.Date(2026, 11, 18, 23, 30, 0, 0, time.UTC)
	ended := started.Add(3 * time.Minute)
	exit := 4

	// The hinted owner corroborates, but its journal then stops at corruption.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","status":"running","owner_session_id":"`+ownerSession+
			`","visible_to_session_id":"`+ownerSession+`","started_at":"`+started.Format(time.RFC3339)+`"}`+"\n",
		"{this is not json}\n",
	)

	// The scoped copy holds the complete observed lifecycle.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_forwarded",
	})
	closeJobJournal(t, rootStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the observed finish")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: a corroborated owner whose prefix stops at corruption cannot overrule an observed finish",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the observed finish", job.ExitCode, exit)
	}
}

// TestLoadSessionJobOutputPageRoutesLegacyBySupplyingJournal pins supplying-journal
// routing. A legacy row whose owner claim the hinted journal could
// NOT corroborate is served from the scoped journal, so its default log lives
// beside that journal. Routing the output by the rejected claim looked in a
// directory that never held this job's log.
func TestLoadSessionJobOutputPageRoutesLegacyBySupplyingJournal(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	scopedSession := identifier.MustNewSessionID()
	uncorroboratedSession := identifier.MustNewSessionID()
	jobID := "job_legacy_a9b8c7d6"
	started := time.Date(2026, 11, 18, 23, 45, 0, 0, time.UTC)

	// The scoped journal holds the row, with a claim naming a session that has no
	// journal at all — so the claim cannot stand.
	writeJobJournalLines(t, jobsDir(stateDir, scopedSession),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","status":"running","owner_session_id":"`+uncorroboratedSession+
			`","visible_to_session_id":"`+scopedSession+`","started_at":"`+started.Format(time.RFC3339)+`"}`+"\n",
	)

	logPath := filepath.Join(jobsDir(stateDir, scopedSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("scoped log"), 0o644); err != nil {
		t.Fatal(err)
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, scopedSession, jobID, nil, 64, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the log beside the journal that supplied the record")
	}
	if page.Data != "scoped log" {
		t.Fatalf("data = %q, want %q: an uncorroborated claim must not choose the log directory",
			page.Data, "scoped log")
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverTornOwnerTail pins the torn-tail
// half of the invariant the scan-damage test pins: a record that is not fully
// established must not outrank
// one that is. The owner's journal holds a start and then an unterminated append,
// so the scan returns a NIL error and only the TornTail diagnostic. Treating
// that nil error as proof of settled state let a running prefix replace the
// ancestor's observed finish and lose its exit code.
func TestLoadSessionJobGetKeepsForwardedTerminalOverTornOwnerTail(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 0, 30, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exit := 7

	// The owner's journal: a valid start, then an append that never terminated.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`,
	)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_forwarded",
	})
	closeJobJournal(t, rootStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the observed finish")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: a torn owner tail is incomplete evidence, not proof of a running job",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the observed finish", job.ExitCode, exit)
	}
	if !slices.Contains(job.Diagnostics, "journal_torn_tail") {
		t.Fatalf("Diagnostics = %v, want journal_torn_tail: the owner's damage must still be reported", job.Diagnostics)
	}
}

// TestLoadSessionJobGetRejectsContradictoryLegacyHint pins corroboration.
// The hinted journal contains the job ID but its row names a
// DIFFERENT owner, so it is a forwarded copy living in another ancestor, not the
// hinted owner's own record. Treating the mere presence of the job ID as
// corroboration let that running copy replace the scoped journal's finish and
// clear the fallback flag.
func TestLoadSessionJobGetRejectsContradictoryLegacyHint(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	scopedSession := identifier.MustNewSessionID()
	hintedSession := identifier.MustNewSessionID()
	thirdSession := identifier.MustNewSessionID()
	jobID := "job_legacy_c1d2e3f4"
	started := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// The scoped journal: the claimed hint, plus the job's completed lifecycle.
	scopedStore := openJobJournal(t, jobsDir(stateDir, scopedSession))
	appendJobEvent(t, scopedStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: hintedSession, VisibleToSession: scopedSession,
		StartedAt: &started,
	})
	appendJobEvent(t, scopedStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen_scoped",
	})
	closeJobJournal(t, scopedStore)

	// The hinted journal has a row for this job that names a THIRD owner: it is a
	// forwarded copy, not the hinted owner's record.
	hintedStore := openJobJournal(t, jobsDir(stateDir, hintedSession))
	appendJobEvent(t, hintedStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: thirdSession, VisibleToSession: hintedSession,
		StartedAt: &started,
	})
	closeJobJournal(t, hintedStore)

	job, found, err := LoadSessionJobGet(stateDir, scopedSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the scoped journal's completed record")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q: a row naming another owner cannot corroborate the hinted one",
			job.Status, jobstore.StatusCompleted)
	}
	if !slices.Contains(job.Diagnostics, "forwarded_fallback") {
		t.Fatalf("Diagnostics = %v, want forwarded_fallback: corroboration failed, so the scoped copy is fallback evidence",
			job.Diagnostics)
	}
}

// TestLoadSessionJobGetPreservesLaunchFieldsFromScopedStart pins the launch-field
// merge: an owner journal holding ONLY a finish states the job's end and nothing
// about what was launched. The
// saved job pane lost the command, type and start time it could previously read.
// The owner's terminal facts must win, and the launch facts must survive.
func TestLoadSessionJobGetPreservesLaunchFieldsFromScopedStart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
	ended := started.Add(4 * time.Minute)
	exit := 0

	// The scoped journal holds the launch details.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		Command: "make build-everything", Description: "full build", StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	// The owner's journal holds ONLY the finish.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's finish")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q from the owner's finish", job.Status, jobstore.StatusCompleted)
	}
	if job.Command != "make build-everything" {
		t.Fatalf("command = %q, want %q from the scoped start: an end alone cannot say what was launched",
			job.Command, "make build-everything")
	}
	if job.Type != string(jobstore.JobShell) {
		t.Fatalf("type = %q, want %q from the scoped start", job.Type, jobstore.JobShell)
	}
	if job.StartedAt == "" {
		t.Fatal("startedAt is empty, want the scoped start's time")
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverStatuslessOwnerRow is invariant
// A applied to the branch the torn tail did not cover: the owner journal holds
// only a NOTIFICATION for the job. The fold creates a row for it because the job
// was named, not because anyone said what it did, so the row has no status, no
// end time and no exit code. A clean scan of a journal that never described the
// job's state proves nothing about that state, and the row may not displace the
// scoped journal's observed finish.
func TestLoadSessionJobGetKeepsForwardedTerminalOverStatuslessOwnerRow(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Minute)
	exit := 9

	// The owner's journal: a notification and nothing else. Applying it creates an
	// empty-status row and reports an incomplete lifecycle.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_notification_pending","seq":1,"job_id":"`+jobID+`","terminal_generation":"gen_owner"}`+"\n",
	)

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_forwarded",
	})
	closeJobJournal(t, rootStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the observed finish")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: a row that never stated a status cannot displace an observed finish",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the observed finish", job.ExitCode, exit)
	}
	if job.EndedAt == "" {
		t.Fatal("endedAt is empty, want the observed finish's end time")
	}
	if !slices.Contains(job.Diagnostics, "incomplete_lifecycle") {
		t.Fatalf("Diagnostics = %v, want incomplete_lifecycle: the owner's diagnostic must still travel", job.Diagnostics)
	}
}

// TestLoadSessionJobGetPreservesLaunchFieldsFromLegacyOwner is invariant B
// applied to the legacy branch: the scoped journal holds the start and the
// corroborated hint journal holds only the finish. The owner's terminal facts
// win and the launch facts fill from the scoped start, exactly as the
// encoded-owner branch already does.
func TestLoadSessionJobGetPreservesLaunchFieldsFromLegacyOwner(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	scopedSession := identifier.MustNewSessionID()
	hintedSession := identifier.MustNewSessionID()
	jobID := "job_legacy_e5f6a7b8"
	started := time.Date(2026, 11, 19, 4, 30, 0, 0, time.UTC)
	ended := started.Add(5 * time.Minute)
	exit := 0
	outputPath := filepath.Join(t.TempDir(), "explicit.log")

	// The scoped journal holds the start, with the launch details and the hint.
	scopedStore := openJobJournal(t, jobsDir(stateDir, scopedSession))
	appendJobEvent(t, scopedStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: hintedSession, VisibleToSession: scopedSession,
		Command: "make legacy-build", WorkingDir: "/tmp/legacy", OutputPath: outputPath,
		StartedAt: &started,
	})
	closeJobJournal(t, scopedStore)

	// The corroborating hinted journal holds only the finish.
	hintedStore := openJobJournal(t, jobsDir(stateDir, hintedSession))
	appendJobEvent(t, hintedStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_legacy_owner",
	})
	closeJobJournal(t, hintedStore)

	job, found, err := LoadSessionJobGet(stateDir, scopedSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the corroborated owner's finish")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q from the owner's finish", job.Status, jobstore.StatusCompleted)
	}
	if job.Command != "make legacy-build" {
		t.Fatalf("command = %q, want %q from the scoped start: an end alone cannot say what was launched",
			job.Command, "make legacy-build")
	}
	if job.Type != string(jobstore.JobShell) {
		t.Fatalf("type = %q, want %q from the scoped start", job.Type, jobstore.JobShell)
	}
	if job.StartedAt == "" {
		t.Fatal("startedAt is empty, want the scoped start's time")
	}
	// The wire type carries no working directory or output path, so those two are
	// checked on the record the read selected.
	read, err := loadSessionJobRecord(stateDir, scopedSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if read.record == nil {
		t.Fatal("record = nil, want the corroborated owner's finish")
	}
	if read.record.WorkingDir != "/tmp/legacy" {
		t.Fatalf("workingDir = %q, want %q from the scoped start", read.record.WorkingDir, "/tmp/legacy")
	}
	if read.record.OutputPath != outputPath {
		t.Fatalf("outputPath = %q, want %q from the scoped start", read.record.OutputPath, outputPath)
	}
}

// TestLoadSessionJobGetReportsCorruptOwnerJournalAsDamage is the low: an encoded
// owner journal that hits a malformed line before decoding any record for this
// job must still hand its damage report back, so the caller serving the
// forwarded copy can say the owner's history is CORRUPT rather than absent.
func TestLoadSessionJobGetReportsCorruptOwnerJournalAsDamage(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 5, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// The owner's journal is unreadable before it names this job at all.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession), "{not a job event}\n")

	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "gen_forwarded",
	})
	closeJobJournal(t, rootStore)

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the forwarded copy as the surviving evidence")
	}
	if read.record.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %q, want %q: the scoped finish is the only settled evidence",
			read.record.Status, jobstore.StatusCompleted)
	}
	if !read.diagnostics.ScanIncomplete {
		t.Fatal("ScanIncomplete = false, want the owner journal's damage reported: a corrupt owner journal is not a missing one")
	}
}

// TestLoadSessionJobOutputPageRoutesToCorroboratedOwnerLog pins corroborated
// owner routing. A legacy job's hinted owner CORROBORATES ownership — its
// journal holds this job — but a torn tail means its row cannot state the job's
// status, so the scoped journal's observed finish is the selected record. The
// two questions come apart: the status came from the scoped journal, and the
// bytes were written to the owner's directory. Routing by the journal that
// supplied the status opened a directory holding no log.
func TestLoadSessionJobOutputPageRoutesToCorroboratedOwnerLog(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	scopedSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := "job_legacy_f7a8b9c0"
	started := time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exit := 0

	// The owner's journal corroborates ownership but is torn, so its running row
	// cannot state the job's status.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`,
	)

	// The scoped journal holds the observed finish and the owner hint.
	scopedStore := openJobJournal(t, jobsDir(stateDir, scopedSession))
	appendJobEvent(t, scopedStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: scopedSession,
		StartedAt: &started,
	})
	appendJobEvent(t, scopedStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_scoped",
		OutputBytes: int64(len("owner log")),
	})
	closeJobJournal(t, scopedStore)

	// The log lives in the OWNER's directory, because the owner ran the job.
	ownerLogPath := filepath.Join(jobsDir(stateDir, ownerSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(ownerLogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerLogPath, []byte("owner log"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decoy in the scoped directory: same job ID, bytes from a different job.
	scopedLogPath := filepath.Join(jobsDir(stateDir, scopedSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(scopedLogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Same length as the owner's log, so a wrong route fails on the CONTENT it read
	// rather than on a size mismatch.
	if err := os.WriteFile(scopedLogPath, []byte("other log"), 0o644); err != nil {
		t.Fatal(err)
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, scopedSession, jobID, nil, 64, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the corroborated owner's log")
	}
	if page.Data != "owner log" {
		t.Fatalf("data = %q, want %q: the log lives in the corroborated owner's directory, not the "+
			"directory of the journal that supplied the status", page.Data, "owner log")
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

// TestLoadSessionJobGetReportsUnresolvedScopeOnTornTail pins the difference
// between "this job is not here" and "this journal cannot say". The requested
// session's journal is an unterminated append whose intact prefix mentions no
// other job, so the torn line could BE the requested job's record. Answering
// not-found asserts an absence the scan never established, and the hub turns
// that into a permanent "job not found". The condition is retryable, so it must
// surface as unavailable.
func TestLoadSessionJobGetReportsUnresolvedScopeOnTornTail(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	otherJob := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

	// A complete record for a DIFFERENT job, then an append that never ended.
	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(otherJob, sessionID, started)+"\n",
		`{"kind":"job_started","seq":2,"job_id":"`+jobID+`","type":"shell"`,
	)

	_, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, nil)
	if found {
		t.Fatal("found = true, want the torn append to leave the job unresolved")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want a wire error: an unresolved scope is retryable, not a permanent miss", err)
	}
	if wireErr.Code != appwire.CodeUnavailable {
		t.Fatalf("error code = %d, want %d (unavailable): not-found would assert absence the scan never established",
			wireErr.Code, appwire.CodeUnavailable)
	}
}

// TestLoadSessionJobGetMarksOnlyUnestablishedStatusesUnconfirmed pins the split
// between "this history has damage" and "this STATUS is not established".
//
// Half one: the ancestor's journal is torn, so its diagnostic rides every read
// that consulted it — but the owner's record was decoded from an intact
// journal, and a client that treated the inherited warning as doubt about the
// status would stop following a healthy running job and miss its finish.
//
// Half two: the record's OWN journal is torn, so the running row is a statement
// about the prefix. That is the case the signal exists for, and a client that
// keeps polling asks a question the journal cannot answer.
func TestLoadSessionJobGetMarksOnlyUnestablishedStatusesUnconfirmed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	// The ancestor's journal: a valid forwarded start, then damage that never
	// terminated. Its diagnostic is real and must keep travelling.
	writeJobJournalLines(t, jobsDir(stateDir, rootSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,`,
	)
	// The owner's journal is intact and says the job is running.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ownerStore)

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's running record")
	}
	if !slices.Contains(job.Diagnostics, "journal_torn_tail") {
		t.Fatalf("Diagnostics = %v, want the ancestor's torn tail still reported", job.Diagnostics)
	}
	if job.StatusUnconfirmed {
		t.Fatal("StatusUnconfirmed = true: the selected record was decoded from an intact journal, so its status IS established")
	}

	// The owner's own journal is now the torn one. Its surviving running row
	// cannot say whether the job finished, so the status is unconfirmed.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`,
	)
	job, found, err = LoadSessionJobGet(stateDir, ownerSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet on the torn owner journal: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's running prefix")
	}
	if !job.StatusUnconfirmed {
		t.Fatalf("StatusUnconfirmed = false (diagnostics %v): the supplying journal is torn, so this running row cannot state the job's state",
			job.Diagnostics)
	}
}

// TestLoadSessionJobGetKeepsNeighbourLifecycleGapOffThisRecord pins the other
// direction of the same split. FoldChecked reports the FIRST structural gap in
// the journal, not necessarily this job's: a healthy running job sharing a
// journal with an orphan finish carries incomplete_lifecycle while its own
// lifecycle is whole. Marking it unconfirmed stops a client following a job
// that is genuinely running, so the signal must be scoped to the record.
func TestLoadSessionJobGetKeepsNeighbourLifecycleGapOffThisRecord(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	otherJob := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	// This job starts cleanly; a DIFFERENT job has a finish with no start.
	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(jobID, sessionID, started)+"\n",
		`{"kind":"job_finished","seq":2,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+otherJob+`","status":"completed","terminal_generation":"gen_orphan"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want this job's running row")
	}
	if !slices.Contains(job.Diagnostics, "incomplete_lifecycle") {
		t.Fatalf("Diagnostics = %v, want the neighbour's gap still reported", job.Diagnostics)
	}
	if job.StatusUnconfirmed {
		t.Fatal("StatusUnconfirmed = true: the gap belongs to another job, and this record's own lifecycle is whole")
	}
}

// TestLoadSessionJobOutputPageRechecksFenceAtLogBoundary pins the window between
// the scope fence and the bytes. The fence is consulted before either journal is
// scanned, so a deletion recorded DURING that scan is invisible to it and the
// page would be served from a session the hub has already committed to removing.
// Rechecking at the boundary where the log is opened closes it, and because an
// abandoned deletion clears its fence, a later read resumes on its own.
func TestLoadSessionJobOutputPageRechecksFenceAtLogBoundary(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
	)
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)
	ownerLogPath := filepath.Join(jobsDir(stateDir, ownerSession), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(ownerLogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerLogPath, []byte("secret bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The deletion becomes durable between the first consultation and the log
	// read: the fence answers clean on the owner once and fenced afterwards.
	consulted := 0
	fence := func(sessionID string) (bool, error) {
		if sessionID != ownerSession {
			return false, nil
		}
		consulted++
		return consulted > 1, nil
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, rootSession, jobID, nil, 64, fence)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if found || page.Data != "" {
		t.Fatalf("found = %v, data = %q: a deletion that became durable during the scan must discard the page",
			found, page.Data)
	}

	// Abandoning the deletion clears the fence, and the same read resumes.
	abandoned := func(string) (bool, error) { return false, nil }
	page, found, err = LoadSessionJobOutputPage(stateDir, rootSession, jobID, nil, 64, abandoned)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage after abandonment: %v", err)
	}
	if !found || page.Data != "secret bytes" {
		t.Fatalf("found = %v, data = %q, want the log back once the deletion is abandoned", found, page.Data)
	}
}

// TestLoadSessionJobGetRejectsMalformedOwnerHint covers the half the
// contradiction test does not reach: the hint is read back OUT of a stored
// row, so it is untrusted input. A value that is not a session id must not be
// handed to the deletion fence or turned into a path. The scoped record stands
// as fallback evidence and the fence only ever hears the requested session.
func TestLoadSessionJobGetRejectsMalformedOwnerHint(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	scopedSession := identifier.MustNewSessionID()
	jobID := "job_legacy_b4a1d0c9"
	started := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	const malformedHint = "not/a/session"

	writeJobJournalLines(t, jobsDir(stateDir, scopedSession),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","status":"running","owner_session_id":"`+malformedHint+
			`","visible_to_session_id":"`+scopedSession+`","started_at":"`+started.Format(time.RFC3339)+`"}`+"\n",
	)

	var consulted []string
	fence := func(sessionID string) (bool, error) {
		consulted = append(consulted, sessionID)
		return false, nil
	}

	job, found, err := LoadSessionJobGet(stateDir, scopedSession, jobID, fence)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the scoped journal's start")
	}
	if !slices.Contains(job.Diagnostics, "forwarded_fallback") {
		t.Fatalf("Diagnostics = %v, want forwarded_fallback: a malformed claim corroborates nothing", job.Diagnostics)
	}
	for _, sessionID := range consulted {
		if sessionID == malformedHint {
			t.Fatalf("deletion fence consulted %q: a stored claim that is not a session id must be validated "+
				"before the fence or a filesystem lookup sees it", sessionID)
		}
	}
}

// TestLoadSessionJobGetWithholdsMetadataFencedDuringScan pins the window between
// the last check and the answer. Every fence check inside loadSessionJobRecord
// precedes the journal scans, so a deletion that becomes durable DURING a scan
// is invisible to them and the metadata leaves the process anyway. The recheck
// immediately before returning closes it, and because an abandoned deletion
// clears its record, the next read succeeds rather than locking out.
func TestLoadSessionJobGetWithholdsMetadataFencedDuringScan(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(jobID, sessionID, started)+"\n",
	)

	// Clean on the first consultation, fenced from the second onward: the
	// deletion lands while the journal is being scanned.
	consulted := 0
	fence := func(string) (bool, error) {
		consulted++
		return consulted > 1, nil
	}

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, fence)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if found {
		t.Fatalf("found = true (status %q): a deletion that became durable during the scan must withhold the row", job.Status)
	}

	abandoned := func(string) (bool, error) { return false, nil }
	job, found, err = LoadSessionJobGet(stateDir, sessionID, jobID, abandoned)
	if err != nil {
		t.Fatalf("LoadSessionJobGet after abandonment: %v", err)
	}
	if !found || job.Status == "" {
		t.Fatalf("found = %v, status = %q: an abandoned deletion must not lock the read out", found, job.Status)
	}
}

// TestLoadSessionJobGetMarksContradictoryOwnerClaimUnconfirmed pins the record's
// OWN claim as a confidence input. A saved running start that names an owner its
// job ID contradicts was folded from a launch describing some other session, so
// its "running" is not a statement about this job — yet the row has a type, so a
// lifecycle-only rule calls it confirmed and the browser follows it. Reading the
// job through the session its ID encodes is exactly the path that bypasses the
// authority predicate, which is why the confidence signal has to catch it.
func TestLoadSessionJobGetMarksContradictoryOwnerClaimUnconfirmed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ownerSession := identifier.MustNewSessionID()
	claimant := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)

	// Read through the encoded owner, whose journal holds a start naming another
	// session as the owner.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, claimant, started)+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, ownerSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the contradictory row's readable history preserved")
	}
	if job.Status == "" {
		t.Fatal("status empty, want the record's history still readable")
	}
	if !job.StatusUnconfirmed {
		t.Fatal("StatusUnconfirmed = false: the record names an owner its job ID contradicts, so its running status is not established")
	}
}

// TestLoadSessionJobGetMarksStatuslessFragmentUnconfirmed pins the invariant on
// the row the fold creates for a job a journal only NAMES. A journal holding
// nothing but a notification for the requested job folds a nonterminal row with
// no status, no end time and no exit code, plus an incomplete-lifecycle
// diagnostic. Nothing in that journal ever stated what the job did, so a client
// that reads the row as confirmed polls it at the live rate forever. A record
// whose status was never established is unconfirmed, not confirmed by absence —
// and the row's diagnostics and any readable output stay available either way.
func TestLoadSessionJobGetMarksStatuslessFragmentUnconfirmed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		`{"kind":"job_notification_pending","seq":1,"job_id":"`+jobID+`","terminal_generation":"gen_only"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the named job's fragment")
	}
	if job.Status != "" {
		t.Fatalf("status = %q, want the empty status the journal supplied", job.Status)
	}
	if !slices.Contains(job.Diagnostics, "incomplete_lifecycle") {
		t.Fatalf("Diagnostics = %v, want incomplete_lifecycle preserved alongside the confidence signal", job.Diagnostics)
	}
	if !job.StatusUnconfirmed {
		t.Fatal("StatusUnconfirmed = false: the journal named the job without ever stating its status, so that status is not established")
	}
}
