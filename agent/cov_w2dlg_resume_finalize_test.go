package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// delegateJobManagerClosing treats a nil manager as closing so a lost runtime
// stops the finalize retry loop rather than spinning.
func TestW2Dlg_DelegateJobManagerClosing_Nil(t *testing.T) {
	t.Parallel()
	if !delegateJobManagerClosing(nil) {
		t.Fatal("delegateJobManagerClosing(nil) = false, want true")
	}
}

// persistDelegateResumability is a no-op for nil/non-delegate running jobs.
func TestW2Dlg_PersistDelegateResumability_Guards(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	jm := s.jobManager

	if err := s.persistDelegateResumability(jm, nil); err != nil {
		t.Fatalf("nil run = %v, want nil", err)
	}
	shell := &runningJob{rec: &jobstore.JobRecord{JobID: "job_s", Type: jobstore.JobShell}}
	if err := s.persistDelegateResumability(jm, shell); err != nil {
		t.Fatalf("shell run = %v, want nil", err)
	}
}

// finalizeDelegate surfaces the job-manager-unavailable failure when the session
// has no job manager.
func TestW2Dlg_FinalizeDelegate_NoJobManager(t *testing.T) {
	t.Parallel()
	if err := (&Session{}).finalizeDelegate("job_x", "child_x", nil); err == nil {
		t.Fatal("finalizeDelegate no job manager: want error")
	}
}

// resumeOrFindRunningDelegate, when the child is already running, resolves the
// active job by transcript ref and surfaces a store read failure.
func TestW2Dlg_ResumeOrFindRunningDelegate_RunningStoreError(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	jm := s.jobManager
	w2dlg_corruptSessionLog(t, s)

	sub := &subagent{running: true}
	run, finalizeErr, active, err := s.resumeOrFindRunningDelegate(
		jm, "child_x", "msg", sub, "ref_x", "dlg_x", nil, nil, false, nil,
	)
	if err == nil {
		t.Fatal("running child with corrupt store: want error")
	}
	if run != nil || finalizeErr != nil || active != nil {
		t.Fatalf("error path returned non-nil results: run=%v finalizeErr=%v active=%v", run, finalizeErr, active)
	}
}

// findRunningDelegateByTranscriptRef surfaces the store read error underneath the
// running-delegate lookup.
func TestW2Dlg_FindRunningDelegateByTranscriptRef_StoreError(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))
	if _, err := findRunningDelegateByTranscriptRef(jm, "ref_x"); err == nil {
		t.Fatal("corrupt store: want error")
	}
}
