package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestS1Cov_validateWatchTarget_CorruptLog(t *testing.T) {
	jm := newTestJM(t)
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_seed",
		Type: jobstore.JobShell, OwnerSessionID: jm.sessionID, StartedAt: &now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
	if err := jm.validateWatchTarget("job_absent"); err == nil {
		t.Fatal("corrupt log must fail target validation")
	}
}

func TestS1Cov_terminalWatchTargetStatus(t *testing.T) {
	t.Run("session_target", func(t *testing.T) {
		jm := newTestJM(t)
		status, terminal, err := jm.terminalWatchTargetStatus(runtimeMessageAliasCaller)
		if err != nil || terminal || status != "" {
			t.Fatalf("session target = %q/%v/%v, want empty/false/nil", status, terminal, err)
		}
	})
	t.Run("running_store_record_not_terminal", func(t *testing.T) {
		jm := newTestJM(t)
		now := jm.now()
		if err := jm.appendEvent(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: now, JobID: "job_run",
			Type: jobstore.JobShell, OwnerSessionID: jm.sessionID, StartedAt: &now,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		status, terminal, err := jm.terminalWatchTargetStatus("job_run")
		if err != nil || terminal || status != "" {
			t.Fatalf("running record = %q/%v/%v, want empty/false/nil", status, terminal, err)
		}
	})
	t.Run("corrupt_log", func(t *testing.T) {
		jm := newTestJM(t)
		now := jm.now()
		if err := jm.appendEvent(jobstore.Event{
			Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x",
			Type: jobstore.JobShell, OwnerSessionID: jm.sessionID, StartedAt: &now,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
		if _, _, err := jm.terminalWatchTargetStatus("job_absent"); err == nil {
			t.Fatal("corrupt log must error")
		}
	})
}
