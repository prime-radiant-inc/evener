package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newAdmissionSession(t *testing.T) (*Session, *jobManager) {
	t.Helper()
	s := newTestSession(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), nil, 0o600); err != nil {
		t.Fatalf("create empty job journal: %v", err)
	}
	jm := &jobManager{dir: dir, sessionID: s.id}
	original := s.jobManager
	s.jobManager = jm
	t.Cleanup(func() { s.jobManager = original })
	return s, jm
}

// Jobs, watches, pending notifications, and unreadable inventory are summary
// data. Only lifecycle-owned child activity participates in done admission.
func TestTerminalAdmissionInventoryIsReadOnly(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {delegateShell: &stableDelegateShellReceipt{jobID: "job-1"}}}
	jm.mu.Unlock()
	work, err := s.outstandingDelegateWork()
	if err != nil || !slices.Contains(work.Jobs, "job-1") {
		t.Fatalf("summary jobs=%v err=%v", work.Jobs, err)
	}
	jm.dir = t.TempDir()
	work, err = s.outstandingDelegateWork()
	if err == nil || !slices.Contains(work.Unknown, "job-journal") || !slices.Contains(work.Jobs, "job-1") {
		t.Fatalf("unknown summary=%+v err=%v", work, err)
	}
}

func TestDelegateWaitOnRunningJobKeepsRunOpenAndResumes(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	if err := s.validateWaitTarget(context.Background(), "job-1"); err != nil {
		t.Fatal(err)
	}
	work, err := s.outstandingDelegateWork()
	if err != nil || !slices.Contains(work.Jobs, "job-1") {
		t.Fatalf("jobs=%v err=%v", work.Jobs, err)
	}
	if s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("running target satisfied wait")
	}
	jm.mu.Lock()
	jm.running = nil
	jm.mu.Unlock()
	if !s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("settled target did not wake wait")
	}
}

func TestDelegateWaitOnSettledJobIsRefusedAtAdmission(t *testing.T) {
	s, _ := newAdmissionSession(t)
	if err := s.validateWaitTarget(context.Background(), "job-1"); err == nil {
		t.Fatal("settled job accepted")
	}
}

func TestDelegateWaitOnRemovedWatchIsRefusedAtAdmission(t *testing.T) {
	s, jm := newAdmissionSession(t)
	key := watchKey{VisibleSessionID: s.id, Target: "job-1", ReceiverSessionID: s.id}
	jm.mu.Lock()
	jm.watches = map[watchKey]*watchConfig{key: {watchID: "watch-1", receiverSessionID: s.id}}
	jm.mu.Unlock()
	if err := s.validateWaitTarget(context.Background(), "watch-1"); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	delete(jm.watches, key)
	jm.mu.Unlock()
	if err := s.validateWaitTarget(context.Background(), "watch-1"); err == nil {
		t.Fatal("removed watch accepted")
	}
}

func TestDelegateWaitOnChildMustBeDescendant(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_self", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_self")
	seedDelegateControllerRunning(t, c, "dlg_sibling", "")
	s := &Session{id: "child-session", delegateController: c, owningDelegateID: "dlg_self"}

	if err := s.validateWaitTarget(context.Background(), "dlg_sibling"); err == nil {
		t.Fatal("a sibling delegate was accepted as a wait target")
	}
	if err := s.validateWaitTarget(context.Background(), "dlg_self"); err == nil {
		t.Fatal("the caller itself was accepted as a wait target")
	}
	if err := s.validateWaitTarget(context.Background(), "dlg_child"); err != nil {
		t.Fatalf("descendant child wait target: %v", err)
	}
}

func TestDelegateWaitSatisfiedByJobWatchAndInput(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	if s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("a running job satisfied the wait")
	}
	jm.mu.Lock()
	delete(jm.running, "job-1")
	jm.mu.Unlock()
	if !s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("a settled job did not satisfy the wait")
	}

	s.inputQueue = []queuedInput{{ID: "input-1"}}
	if !s.terminalWaitSatisfied(context.Background(), "input-1") {
		t.Fatal("queued input did not satisfy the wait")
	}
	s.steeringQueue = []steeringMessage{{StableTurnID: "steer-1", Text: "parent note"}}
	if !s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("parent steering did not satisfy the wait")
	}
}
