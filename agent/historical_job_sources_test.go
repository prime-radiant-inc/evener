package agent

import (
	"encoding/json"
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
