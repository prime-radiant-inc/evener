package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// A delegate send to a concrete delegate handle on a session with no job manager
// fails with the job-manager-unavailable reason.
func TestS1Cov_sendDelegateMessage_NoJobManager(t *testing.T) {
	res := (&Session{}).sendDelegateMessage(context.Background(), sendMessageArgs{Target: "dlg_x", Message: "hi"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), jobManagerUnavailableReason) {
		t.Fatalf("res.Err = %v, want job-manager-unavailable", res.Err)
	}
}

// A corrupt delegate log surfaces the LoadDelegates decode error to the sender.
func TestS1Cov_sendDelegateMessage_LoadDelegatesError(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateTestSession(t, c)
	// Seed one real event so the log exists, then append a garbage line so the
	// store's LoadDelegates read fails.
	now := time.Now().UTC()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_seed",
		Type: jobstore.JobShell, OwnerSessionID: s.ID(), StartedAt: &now,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	path := filepath.Join(s.jobManager.dir, "jobs.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString("{garbage\n"); err != nil {
		t.Fatalf("corrupt log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: "dlg_ghost", Message: "hi"})
	if res.Err == nil {
		t.Fatal("corrupt delegate log must fail the send")
	}
}
