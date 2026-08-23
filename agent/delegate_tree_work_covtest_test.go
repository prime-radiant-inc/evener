package agent

import (
	"context"
	"errors"
	"testing"
)

// TestBeginShellWork_Closing covers the closing=true path (lines 22-23).
func TestBeginShellWork_Closing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	_, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy, got %v", err)
	}
}

// TestBeginShellWork_StaleLease covers the admitLeaseLocked error path
// (lines 25-26) when the lease doesn't match a running delegate.
func TestBeginShellWork_StaleLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// Use wrong generation.
	_, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 99})
	if err == nil {
		t.Fatal("expected error for stale lease")
	}
}

// TestCommitShellWork_StaleLease_NilWork covers the stale-lease path when
// the work token doesn't exist (lines 38-40).
func TestCommitShellWork_StaleLease_NilWork(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// Token doesn't exist.
	_, err := c.CommitShellWork(delegateWorkToken{processID: 999}, "job1", func() {})
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease, got %v", err)
	}
}

// TestCommitShellWork_StaleLease_EmptyJobID covers the stale-lease path when
// the shellJobID is empty (lines 38-40).
func TestCommitShellWork_StaleLease_EmptyJobID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	_, err = c.CommitShellWork(token, "", func() {})
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for empty jobID, got %v", err)
	}
}

// TestCommitShellWork_StaleLease_NilCancel covers the stale-lease path when
// cancel is nil (lines 38-40).
func TestCommitShellWork_StaleLease_NilCancel(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	_, err = c.CommitShellWork(token, "job1", nil)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for nil cancel, got %v", err)
	}
}

// TestCommitShellWork_ClosingNotStopCovered covers the closing path where
// closing=true but the delegate is not covered by any stop (lines 42-49).
func TestCommitShellWork_ClosingNotStopCovered(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	cancelNow, err := c.CommitShellWork(token, "job1", func() {})
	if err != nil {
		t.Fatalf("CommitShellWork: %v", err)
	}
	if !cancelNow {
		t.Fatal("expected cancelNow=true when closing")
	}
}

// TestAbortShellWork_NotFound covers the stale-lease path in AbortShellWork
// (lines 65-67) when the work token doesn't exist.
func TestAbortShellWork_NotFound(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	err := c.AbortShellWork(delegateWorkToken{processID: 999})
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease, got %v", err)
	}
}

// TestAbortShellWork_AlreadyCommitted covers the committed path in
// AbortShellWork (lines 65-67).
func TestAbortShellWork_AlreadyCommitted(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if _, err := c.CommitShellWork(token, "job1", func() {}); err != nil {
		t.Fatalf("CommitShellWork: %v", err)
	}
	err = c.AbortShellWork(token)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease, got %v", err)
	}
}

// TestCommitShellWork_AlreadyCommitted covers the double-commit path
// (lines 38-40).
func TestCommitShellWork_AlreadyCommitted(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	token, err := c.BeginShellWork(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	cancel := context.CancelFunc(func() {})
	if _, err := c.CommitShellWork(token, "job1", cancel); err != nil {
		t.Fatalf("first CommitShellWork: %v", err)
	}
	_, err = c.CommitShellWork(token, "job1", cancel)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for double commit, got %v", err)
	}
}
