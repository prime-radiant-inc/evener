//go:build serffuzz

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJobDelegateExactTailRunning exercises the defensive edges around finding
// and steering an already-running delegate. All state is test-owned and local;
// no provider, subprocess, clock, or network boundary is involved.
func FuzzJobDelegateExactTailRunning(f *testing.F) {
	for seed := byte(0); seed < 7; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		switch seed % 7 {
		case 0:
			jm := newTestJM(t)
			corruptDelegateTailStore(t, jm)
			if _, err := findRunningDelegateByTranscriptRef(jm, "local:child"); err == nil {
				t.Fatal("corrupt running-delegate store lookup succeeded")
			}
		case 1:
			s := newTestSession(t)
			res := s.sendRunningDelegateMessage("job_bad_ref", "message", &jobstore.JobRecord{
				JobID: "job_bad_ref", TranscriptRef: "malformed",
			}, false, nil)
			requireDelegateTailError(t, res, "target_not_resumable: invalid transcript_ref")
		case 2:
			s := newTestSession(t)
			res := s.sendRunningDelegateMessage("job_missing_child", "message", &jobstore.JobRecord{
				JobID: "job_missing_child", TranscriptRef: "local:missing_child",
			}, false, nil)
			requireDelegateTailError(t, res, "target_not_resumable: delegate session")
		case 3:
			child := newTestSession(t)
			s := &Session{subagents: newSubagentManager(nil)}
			s.subagents.track(delegateTailSub(child, true))
			res := s.sendRunningDelegateMessage("job_no_manager", "message", delegateTailRecord("job_no_manager", child.ID()), true, nil)
			requireDelegateTailError(t, res, jobManagerUnavailableReason)
		case 4:
			s := newTestSession(t)
			child := newTestSession(t)
			s.subagents.track(delegateTailSub(child, true))
			res := s.sendRunningDelegateMessage("job_runtime_gone", "message", delegateTailRecord("job_runtime_gone", child.ID()), true, nil)
			requireDelegateTailError(t, res, "not_controllable: delegate job")
			if !strings.Contains(res.Err.Error(), "runtime job is not live") {
				t.Fatalf("runtime-missing error = %v", res.Err)
			}
		case 5:
			s := newTestSession(t)
			child := newTestSession(t)
			child.Close()
			s.subagents.track(delegateTailSub(child, true))
			res := s.sendRunningDelegateMessage("job_reject_steer", "message", delegateTailRecord("job_reject_steer", child.ID()), false, nil)
			requireDelegateTailError(t, res, "not_controllable: delegate job")
			if !strings.Contains(res.Err.Error(), "not accepting messages") {
				t.Fatalf("rejected-steer error = %v", res.Err)
			}
		case 6:
			s := newTestSession(t)
			child := newTestSession(t)
			sub := delegateTailSub(child, true)
			corruptDelegateTailStore(t, s.jobManager)
			_, _, _, err := s.resumeOrFindRunningDelegate(
				s.jobManager, child.ID(), "message", sub, "local:"+child.ID(),
				"dlg_running", nil, nil, false, nil,
			)
			if err == nil || !strings.Contains(err.Error(), "active job is unknown") {
				t.Fatalf("running delegate lookup error = %v", err)
			}
		}
	})
}

func delegateTailSub(child *Session, running bool) *subagent {
	return &subagent{
		id: child.ID(), sess: child, running: running,
		status: SubagentRunning, done: make(chan struct{}),
	}
}

func delegateTailRecord(jobID, childID string) *jobstore.JobRecord {
	return &jobstore.JobRecord{
		JobID: jobID, DelegateID: "dlg_tail", Type: jobstore.JobDelegate,
		Status: jobstore.StatusRunning, TranscriptRef: "local:" + childID,
	}
}

func corruptDelegateTailStore(t *testing.T, jm *jobManager) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(jm.dir, "jobs.jsonl"), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatalf("corrupt jobs store: %v", err)
	}
}

func requireDelegateTailError(t *testing.T, result sendMessageResult, fragment string) {
	t.Helper()
	if result.Err == nil || !strings.Contains(result.Err.Error(), fragment) {
		t.Fatalf("send result error = %v, want fragment %q", result.Err, fragment)
	}
}
