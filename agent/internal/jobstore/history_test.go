package jobstore

import (
	"testing"
	"time"
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

func TestMergeJournalsForwardedFallbackIsIncomplete(t *testing.T) {
	tm := time.Now()
	got, d, err := MergeJournals([]JournalSource{{SessionID: "root", Root: true, Available: true, Events: []Event{{Kind: EventJobStarted, Seq: 1, JobID: "j1", Type: JobShell, OwnerSessionID: "gone", StartedAt: &tm}}}})
	if err != nil || got["j1"] == nil || !d.Incomplete || len(d.MissingOwners) != 1 {
		t.Fatalf("got=%+v diagnostics=%+v err=%v", got["j1"], d, err)
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
