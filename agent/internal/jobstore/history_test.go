package jobstore

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

func TestMergeJournalsOwnerWinsRegardlessSourceOrder(t *testing.T) {
	tm := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	owner := []Event{{Kind: EventJobStarted, Seq: 1, JobID: "j1", Type: JobShell, OwnerSessionID: "owner", StartedAt: &tm}, {Kind: EventJobFinished, Seq: 2, JobID: "j1", Status: StatusFailed}}
	forwarded := []Event{{Kind: EventJobStarted, Seq: 1, JobID: "j1", Type: JobShell, OwnerSessionID: "owner", StartedAt: &tm}, {Kind: EventJobFinished, Seq: 2, JobID: "j1", Status: StatusCompleted}}
	for _, sources := range [][]JournalSource{{{SessionID: "root", Root: true, Available: true, Events: forwarded}, {SessionID: "owner", Available: true, Events: owner}}, {{SessionID: "owner", Available: true, Events: owner}, {SessionID: "root", Root: true, Available: true, Events: forwarded}}} {
		got, d, err := MergeJournals(sources)
		if err != nil || got["j1"].Status != StatusFailed || len(d.Mismatches) == 0 || d.Incomplete {
			t.Fatalf("got=%+v diagnostics=%+v err=%v", got["j1"], d, err)
		}
	}
}

func TestMergeJournalsCanonicalMismatchIsDamagedFallback(t *testing.T) {
	owner := "02wMz5TxvEMoJEDTDGOTil"
	id := identifier.MustNewJobID(owner)
	tm := time.Now().UTC()
	forwarded := []Event{{Kind: EventJobStarted, Seq: 1, JobID: id, Type: JobShell, OwnerSessionID: "wrong", StartedAt: &tm}, {Kind: EventJobFinished, Seq: 2, JobID: id, Status: StatusCompleted}}
	got, d, err := MergeJournals([]JournalSource{{SessionID: "root", Root: true, Available: true, Events: forwarded}})
	if err != nil || got[id] == nil || got[id].Authority != AuthorityForwardedFallback || !got[id].Incomplete || len(d.InvalidOwners) == 0 {
		t.Fatalf("got=%+v diagnostics=%+v err=%v", got[id], d, err)
	}
}

func TestMergeJournalsForwardedFallbackIsIncomplete(t *testing.T) {
	tm := time.Now()
	got, d, err := MergeJournals([]JournalSource{{SessionID: "root", Root: true, Available: true, Events: []Event{{Kind: EventJobStarted, Seq: 1, JobID: "j1", Type: JobShell, OwnerSessionID: "gone", StartedAt: &tm}}}})
	if err != nil || got["j1"] == nil || !d.Incomplete || len(d.MissingOwners) != 1 {
		t.Fatalf("got=%+v diagnostics=%+v err=%v", got["j1"], d, err)
	}
}

// TestMergeJournalsExplicitOwnerWithoutEmbeddedIDEmitsNoCompatibility pins the
// benign case: an explicit OwnerSessionID beside a JobID that does not embed an
// owner (e.g. "job_000000") is authoritative, not a compatibility event. Before
// the fix this emitted one Compatibility diagnostic per job, so a large journal
// produced an unbounded diagnostic list that swamped the activity-projection
// envelope and withdrew paging continuations.
func TestMergeJournalsExplicitOwnerWithoutEmbeddedIDEmitsNoCompatibility(t *testing.T) {
	tm := time.Now().UTC()
	events := []Event{
		{Kind: EventJobStarted, Seq: 1, JobID: "job_000000", Type: JobShell, OwnerSessionID: "root", StartedAt: &tm},
		{Kind: EventJobFinished, Seq: 2, JobID: "job_000000", Status: StatusCompleted},
	}
	got, d, err := MergeJournals([]JournalSource{{SessionID: "root", Root: true, Available: true, Events: events}})
	if err != nil {
		t.Fatal(err)
	}
	if got["job_000000"] == nil {
		t.Fatalf("record missing: %+v", got)
	}
	if len(d.Compatibility) != 0 || len(d.InvalidOwners) != 0 {
		t.Fatalf("diagnostics = %+v, want no compatibility/invalid-owner entries", d)
	}
	if d.Incomplete {
		t.Fatalf("benign explicit-owner record marked incomplete: %+v", d)
	}
}

func TestMergeJournalsRootCorruptionFatalDescendantRecoverable(t *testing.T) {
	if _, _, err := MergeJournals([]JournalSource{{SessionID: "root", Root: true, Available: true, Diagnostics: ReadDiagnostics{Corrupt: true}}}); err == nil {
		t.Fatal("root corruption was not fatal")
	}
	_, d, err := MergeJournals([]JournalSource{{SessionID: "child", Available: true, Diagnostics: ReadDiagnostics{Corrupt: true}}})
	if err != nil || !d.Incomplete || len(d.CorruptBranches) != 1 {
		t.Fatalf("diagnostics=%+v err=%v", d, err)
	}
}

func TestMergeJournalsAttributesAndDeduplicatesLifecycleIssues(t *testing.T) {
	// Two finishes without a start in one journal: the repeated fragment is
	// attributed to the owner and emitted once.
	events := []Event{
		{Kind: EventJobFinished, Seq: 2, JobID: "j1", Status: StatusCompleted},
		{Kind: EventJobFinished, Seq: 3, JobID: "j1", Status: StatusCompleted},
	}
	_, d, err := MergeJournals([]JournalSource{{SessionID: "owner-a", Available: true, Events: events}})
	if err != nil {
		t.Fatal(err)
	}
	finishWithoutStart := 0
	for _, message := range d.LifecycleErrors {
		if !strings.HasPrefix(message, "owner owner-a: ") {
			t.Fatalf("unattributed lifecycle diagnostic %q", message)
		}
		if strings.Contains(message, "finish without start") {
			finishWithoutStart++
		}
	}
	if finishWithoutStart != 1 {
		t.Fatalf("finish-without-start diagnostics = %d (%v), want 1", finishWithoutStart, d.LifecycleErrors)
	}

	// The same fragment in a second journal stays in the output, attributed to
	// that journal, so each source is distinguishable.
	_, d2, err := MergeJournals([]JournalSource{
		{SessionID: "owner-a", Available: true, Events: events},
		{SessionID: "owner-b", Available: true, Events: events},
	})
	if err != nil {
		t.Fatal(err)
	}
	var attributedA, attributedB bool
	for _, message := range d2.LifecycleErrors {
		if strings.HasPrefix(message, "owner owner-a: ") {
			attributedA = true
		}
		if strings.HasPrefix(message, "owner owner-b: ") {
			attributedB = true
		}
	}
	if !attributedA || !attributedB {
		t.Fatalf("lifecycle diagnostics not attributed per source: %v", d2.LifecycleErrors)
	}
}
