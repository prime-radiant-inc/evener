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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
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

	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID)
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

	job, found, err := LoadSessionJobGet(stateDir, askedSession, jobID)
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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if found {
		t.Fatalf("found = true (status %q): the owner is fenced for deletion, so its job must not be served",
			job.Status)
	}
}

// TestLoadSessionJobGetEchoesRequestedRef is the round-3 medium-1 regression.
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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
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

// TestLoadSessionJobGetUsesDamagedPrefixAsScope is the round-3 medium-2
// regression. An ancestor journal holding a valid forwarded start followed by a
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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
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

// TestLoadSessionJobGetResolvesLegacyIDByOwnerHint is the round-3 low-4
// regression. A job saved before owner-encoded IDs has a bare UUID job ID and a
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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
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

	read, err := loadSessionJobRecord(stateDir, rootSession, jobID)
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

// TestLoadSessionJobGetSurfacesDiagnostics is the round-3 low-1 regression: a
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

	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
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
