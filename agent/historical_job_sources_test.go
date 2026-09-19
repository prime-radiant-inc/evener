package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestLoadSessionHistoricalJobRecordsAcquiresOwnerJournal(t *testing.T) {
	state, root, owner := t.TempDir(), "root-session", "owner-session"
	tm := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write := func(id string, events []jobstore.Event) {
		d := filepath.Join(state, "sessions", id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(d, "jobs.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, e := range events {
			if err := json.NewEncoder(f).Encode(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(root, []jobstore.Event{{Kind: jobstore.EventJobStarted, Seq: 1, JobID: "job-child", Type: jobstore.JobShell, OwnerSessionID: owner, StartedAt: &tm}, {Kind: jobstore.EventJobFinished, Seq: 2, JobID: "job-child", Status: jobstore.StatusCompleted}})
	write(owner, []jobstore.Event{{Kind: jobstore.EventJobStarted, Seq: 1, JobID: "job-child", Type: jobstore.JobShell, OwnerSessionID: owner, StartedAt: &tm}, {Kind: jobstore.EventJobFinished, Seq: 2, JobID: "job-child", Status: jobstore.StatusFailed}})
	got, d, err := loadSessionHistoricalJobRecordsWithDiagnostics(state, root)
	if err != nil || got["job-child"].Status != string(jobstore.StatusFailed) || len(d.Mismatches) == 0 {
		t.Fatalf("got=%+v diagnostics=%+v err=%v", got["job-child"], d, err)
	}
}

// A tree that references more missing owners than the source cap must not be
// treated as truncated: unavailable journals do not consume the budget.
func TestLoadRetainedJobHistory_MissingSourcesDoNotCountTowardCap(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootcapmissing"
	tm := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := make([]jobstore.Event, 0, maxRetainedJobSources+8)
	for i := range maxRetainedJobSources + 8 {
		events = append(events, jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			Seq:            int64(i + 1),
			JobID:          fmt.Sprintf("missing-job-%02d", i),
			Type:           jobstore.JobShell,
			OwnerSessionID: fmt.Sprintf("cap-missing-owner-%02d", i),
			StartedAt:      &tm,
		})
	}
	s1cov_writeJobLog(t, stateDir, rootID, events...)

	_, diagnostics, err := loadRetainedJobHistory(stateDir, rootID)
	if err != nil {
		t.Fatalf("loadRetainedJobHistory: %v", err)
	}
	if len(diagnostics.TruncatedSources) != 0 {
		t.Fatalf("TruncatedSources = %v, want none", diagnostics.TruncatedSources)
	}
}

// A root that delegated to more distinct, still-readable descendant sessions
// than the cap must degrade to a truncated-but-usable projection instead of
// failing the whole load.
func TestLoadRetainedJobHistory_AvailableSourceCapTruncatesWithoutError(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootcapavailable"
	tm := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ownerCount := maxRetainedJobSources + 3
	rootEvents := make([]jobstore.Event, 0, ownerCount)
	for i := range ownerCount {
		owner := fmt.Sprintf("cap-owner-%02d", i)
		jobID := fmt.Sprintf("cap-job-%02d", i)
		rootEvents = append(rootEvents, jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			Seq:            int64(i + 1),
			JobID:          jobID,
			Type:           jobstore.JobShell,
			OwnerSessionID: owner,
			StartedAt:      &tm,
		})
		s1cov_writeJobLog(t, stateDir, owner,
			jobstore.Event{Kind: jobstore.EventJobStarted, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: owner, StartedAt: &tm},
		)
	}
	s1cov_writeJobLog(t, stateDir, rootID, rootEvents...)

	records, diagnostics, err := loadRetainedJobHistory(stateDir, rootID)
	if err != nil {
		t.Fatalf("loadRetainedJobHistory with %d available sources: %v", ownerCount, err)
	}
	if len(diagnostics.TruncatedSources) != 1 || diagnostics.TruncatedSources[0] != rootID {
		t.Fatalf("TruncatedSources = %v, want [%s]", diagnostics.TruncatedSources, rootID)
	}
	if !diagnostics.Incomplete {
		t.Fatal("truncated load did not mark diagnostics incomplete")
	}
	if len(records) == 0 {
		t.Fatal("truncated load returned no records")
	}
	// The diagnostic-only loader must also survive the same root.
	if _, _, err := loadSessionHistoricalJobRecordsWithDiagnostics(stateDir, rootID); err != nil {
		t.Fatalf("loadSessionHistoricalJobRecordsWithDiagnostics: %v", err)
	}
}
