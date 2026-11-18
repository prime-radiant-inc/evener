package jobstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// diagTime is a fixed timestamp: the lifecycle checks read structure, and a
// constant keeps the fixtures honest about what they prove.
func diagTime() time.Time {
	return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
}

// TestScanEventsWithDiagnostics_ReportsTornTail is the integrity-contract test
// for a job journal: the torn trailing append ReadEvents has always tolerated
// must be REPORTED, not silently dropped. It mirrors
// delegatestore.ReadDiagnostics{TornTail}, which is what makes retained job and
// delegate history answer the same question the same way.
func TestScanEventsWithDiagnostics_ReportsTornTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"job_started","seq":1,"job_id":"job_A","type":"shell"}` + "\n" +
		`{"kind":"job_finished","seq":2,` // truncated trailing line, no newline
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	events, diagnostics, err := ScanEventsWithDiagnostics(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("a torn trailing append must stay tolerated: %v", err)
	}
	if _, err := FoldChecked(events); err != nil {
		t.Fatalf("the events before a torn tail must still fold: %v", err)
	}
	if !diagnostics.TornTail {
		t.Fatal("ReadDiagnostics.TornTail = false, want true for an unterminated incomplete trailing event")
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (the torn trailing event is excluded)", len(events))
	}
}

// TestScanEventsWithDiagnostics_CleanJournalHasNoTornTail pins the other half:
// a journal that ends on a completed record reports nothing, so a caller
// cannot learn to ignore the diagnostic as always-on.
func TestScanEventsWithDiagnostics_CleanJournalHasNoTornTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"job_started","seq":1,"job_id":"job_A","type":"shell"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, diagnostics, err := ScanEventsWithDiagnostics(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("ScanEventsWithDiagnostics: %v", err)
	}
	if diagnostics.TornTail {
		t.Fatal("ReadDiagnostics.TornTail = true for a journal ending on a complete record")
	}
}

// TestScanEventsAndFoldChecked_ReportOrphanFinish is the lifecycle half of
// the integrity contract: a finish with no preceding start is structurally
// incomplete, and jobstore.Fold's best-effort record must not be presented as
// a coherent history. The error is matched by sentinel, never by text.
func TestScanEventsAndFoldChecked_ReportOrphanFinish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	content := `{"kind":"job_finished","seq":1,"job_id":"job_A","status":"completed"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	events, _, err := ScanEventsWithDiagnostics(context.Background(), path, ScanLimits{})
	if err != nil {
		t.Fatalf("a complete but incoherent journal is a fold problem, not a read problem: %v", err)
	}
	if _, err := FoldChecked(events); !errors.Is(err, ErrIncompleteLifecycle) {
		t.Fatalf("FoldChecked error = %v, want ErrIncompleteLifecycle for a finish with no start", err)
	}
}

// TestFoldChecked_AcceptsWholeLifecycle keeps the check honest in the other
// direction: the ordinary start-then-finish history must fold without a
// complaint, so the sentinel means damage and not merely age.
func TestFoldChecked_AcceptsWholeLifecycle(t *testing.T) {
	t.Parallel()
	started := diagTime()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	events := []Event{
		{Kind: EventJobStarted, Seq: 1, TS: started, JobID: jobID, Type: JobShell, OwnerSessionID: owner, StartedAt: &started},
		{Kind: EventJobFinished, Seq: 2, TS: started, JobID: jobID, Status: StatusCompleted, EndedAt: &started, TerminalGen: "gen_A"},
	}

	recs, err := FoldChecked(events)
	if err != nil {
		t.Fatalf("FoldChecked on a whole lifecycle: %v", err)
	}
	if rec := recs[jobID]; rec == nil || rec.Status != StatusCompleted {
		t.Fatalf("FoldChecked folded %+v, want a completed %s", rec, jobID)
	}
}

// TestFoldChecked_RejectsStartWithoutType covers the other required-field arm:
// a start that cannot say what it started is a fragment. A start whose declared
// owner contradicts its job ID is the owner-identity check the issue asks for.
func TestFoldChecked_RejectsStartWithoutType(t *testing.T) {
	t.Parallel()
	owner := identifier.MustNewSessionID()
	started := diagTime()
	jobID := identifier.MustNewJobID(owner)
	cases := map[string]Event{
		"no type": {Kind: EventJobStarted, Seq: 1, TS: started, JobID: jobID, OwnerSessionID: owner, StartedAt: &started},
		"wrong owner": {
			Kind: EventJobStarted, Seq: 1, TS: started, JobID: jobID,
			Type: JobShell, OwnerSessionID: identifier.MustNewSessionID(), StartedAt: &started,
		},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := FoldChecked([]Event{event}); !errors.Is(err, ErrIncompleteLifecycle) {
				t.Fatalf("FoldChecked(%s) error = %v, want ErrIncompleteLifecycle", name, err)
			}
		})
	}
}

// TestFoldChecked_AcceptsLegacyEmptyOwner pins the compatibility ruling the
// issue's triage warns about: rows written before OwnerSessionID existed fold
// with it unset, and tightening validation must not make every pre-owner
// journal unreadable.
func TestFoldChecked_AcceptsLegacyEmptyOwner(t *testing.T) {
	t.Parallel()
	started := diagTime()
	events := []Event{
		{Kind: EventJobStarted, Seq: 1, TS: started, JobID: "job_legacy", Type: JobShell, StartedAt: &started},
		{Kind: EventJobFinished, Seq: 2, TS: started, JobID: "job_legacy", Status: StatusCompleted, EndedAt: &started, TerminalGen: "gen"},
	}
	if _, err := FoldChecked(events); err != nil {
		t.Fatalf("FoldChecked on a legacy owner-empty lifecycle: %v", err)
	}
}
