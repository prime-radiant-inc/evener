package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// s1cov_seedDelegateWithJob appends a delegate-created event plus one job-started
// event linked to it, letting tests craft the delegate/job ownership and type
// states the watch-send validators branch on.
func s1cov_seedDelegateWithJob(t *testing.T, jm *jobManager, delegateID, jobID string, jobType jobstore.JobType, jobOwner string) {
	t.Helper()
	now := jm.now()
	child := "child_" + delegateID
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   child,
			TranscriptRef:    encodeRef("", child),
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_1",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	if jobID == "" {
		return
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobType,
		DelegateID:       delegateID,
		OwnerSessionID:   jobOwner,
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", child),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("append job: %v", err)
	}
}

func TestS1Cov_validateWatchSendTarget_DelegateArms(t *testing.T) {
	t.Run("no_job_history", func(t *testing.T) {
		jm := newTestJM(t)
		s1cov_seedDelegateWithJob(t, jm, "dlg_nh", "", jobstore.JobDelegate, jm.sessionID)
		if err := jm.validateWatchSendTarget("dlg_nh", watchArgs{}); err == nil || !strings.Contains(err.Error(), "no job history") {
			t.Fatalf("err = %v, want no-job-history", err)
		}
	})
	t.Run("job_owned_by_descendant", func(t *testing.T) {
		jm := newTestJM(t)
		s1cov_seedDelegateWithJob(t, jm, "dlg_desc", "job_desc", jobstore.JobDelegate, "DESCENDANT")
		if err := jm.validateWatchSendTarget("dlg_desc", watchArgs{}); err == nil || !strings.Contains(err.Error(), "not_controllable") {
			t.Fatalf("err = %v, want not_controllable", err)
		}
	})
	t.Run("job_wrong_type", func(t *testing.T) {
		jm := newTestJM(t)
		s1cov_seedDelegateWithJob(t, jm, "dlg_shell", "job_shell", jobstore.JobShell, jm.sessionID)
		if err := jm.validateWatchSendTarget("dlg_shell", watchArgs{}); err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
			t.Fatalf("err = %v, want target_not_messageable", err)
		}
	})
	t.Run("corrupt_log", func(t *testing.T) {
		jm := newTestJM(t)
		s1cov_seedDelegateWithJob(t, jm, "dlg_c", "", jobstore.JobDelegate, jm.sessionID)
		s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
		if err := jm.validateWatchSendTarget("dlg_c", watchArgs{}); err == nil {
			t.Fatal("corrupt log must fail validation")
		}
	})
}

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
