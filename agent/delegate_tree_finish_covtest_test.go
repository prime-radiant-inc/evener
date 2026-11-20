package agent

import (
	"errors"
	"testing"
	"time"
)

// TestSupervisionBoundary_StaleLease covers the exactLease error path
// (lines 44-46).
func TestSupervisionBoundary_StaleLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	lease := delegateLease{delegateID: "nonexistent", generation: 1}
	_, err := c.SupervisionBoundary(lease, delegateSettlementOrdinary)
	if err == nil {
		t.Fatal("expected error for stale lease")
	}
}

// TestSupervisionBoundary_Closing covers the closing/stopping path
// (lines 48-50).
func TestSupervisionBoundary_Closing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	boundary, err := c.SupervisionBoundary(delegateLease{delegateID: "dlg_target", generation: 1}, delegateSettlementOrdinary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if boundary != delegateSupervisionSuppress {
		t.Fatalf("expected suppress for closing, got %v", boundary)
	}
}

// TestSupervisionBoundary_NotRunning covers the not-running path
// (lines 51-52).
func TestSupervisionBoundary_NotRunning(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	_, err := c.SupervisionBoundary(delegateLease{delegateID: "dlg_idle", generation: 1}, delegateSettlementOrdinary)
	if err == nil {
		t.Fatal("expected error for idle delegate (not running)")
	}
}

// TestBeginFinalization_InvalidMode covers the default switch case
// (lines 109-110).
func TestBeginFinalization_InvalidMode(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	_, _, err := c.BeginFinalization(delegateLease{delegateID: "dlg_target", generation: 1}, delegateSettlementMode(99))
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy, got %v", err)
	}
}

// TestBeginFinalization_StaleLease_Terminal covers the exactLease error path
// for terminal mode (lines 98-100).
func TestBeginFinalization_StaleLease_Terminal(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	_, _, err := c.BeginFinalization(delegateLease{delegateID: "nonexistent", generation: 1}, delegateSettlementTerminal)
	if err == nil {
		t.Fatal("expected error for stale lease in terminal mode")
	}
}

// TestBeginFinalization_StaleLease_Ordinary covers the admitLease error path
// for ordinary mode with no stop (lines 75-82).
func TestBeginFinalization_StaleLease_Ordinary(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	_, _, err := c.BeginFinalization(delegateLease{delegateID: "nonexistent", generation: 1}, delegateSettlementOrdinary)
	if err == nil {
		t.Fatal("expected error for stale lease in ordinary mode")
	}
}

// TestBeginFinalization_TerminalNotRunningOrStopping covers the phase check
// (lines 102-103).
func TestBeginFinalization_TerminalNotRunningOrStopping(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	_, _, err := c.BeginFinalization(delegateLease{delegateID: "dlg_idle", generation: 1}, delegateSettlementTerminal)
	if err == nil {
		t.Fatal("expected error for idle delegate in terminal mode")
	}
}

// TestCompleteSettlement_NilClaim covers the nil-claim path.
func TestCompleteSettlement_NilClaim(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	_, err := c.CompleteSettlement(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil claim")
	}
}

// TestCompleteSettlement_StaleClaim covers the stale-claim path.
func TestCompleteSettlement_StaleClaim(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	claim := &delegateSettlementClaim{token: 99999, lease: delegateLease{delegateID: "dlg_target", generation: 1}}
	_, err := c.CompleteSettlement(claim, nil)
	if err == nil {
		t.Fatal("expected error for stale claim")
	}
}

// TestReportActivity_ZeroAt covers the at.IsZero() path (lines 88-90).
func TestReportActivity_ZeroAt(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// Call with zero time — should use c.now() instead.
	err := c.ReportActivity(delegateLease{delegateID: "dlg_target", generation: 1}, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
