package agent

import (
	"testing"
)

// TestClaimRuntimeReclamation_NilController covers the nil-controller guard
// (lines 38-39).
func TestClaimRuntimeReclamation_NilController(t *testing.T) {
	var c *delegateTreeController
	_, err := c.ClaimRuntimeReclamation(1)
	if err == nil {
		t.Fatal("expected error for nil controller")
	}
}

// TestClaimRuntimeReclamation_ZeroRequired covers the required<=0 path
// (lines 41-42).
func TestClaimRuntimeReclamation_ZeroRequired(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim, err := c.ClaimRuntimeReclamation(0)
	if err != nil || claim != nil {
		t.Fatalf("expected nil claim, nil error for required=0, got claim=%v err=%v", claim, err)
	}
	claim, err = c.ClaimRuntimeReclamation(-1)
	if err != nil || claim != nil {
		t.Fatalf("expected nil claim, nil error for required<0, got claim=%v err=%v", claim, err)
	}
}

// TestClaimRuntimeReclamation_Closing covers the closing path (lines 46-47).
func TestClaimRuntimeReclamation_Closing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	_, err := c.ClaimRuntimeReclamation(1)
	if err == nil {
		t.Fatal("expected error for closing controller")
	}
}

// TestClaimRuntimeReclamation_NoResident covers the needed<=0 path
// (lines 56-58) where there are no resident terminal runtimes to reclaim.
func TestClaimRuntimeReclamation_NoResident(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	claim, err := c.ClaimRuntimeReclamation(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claim != nil {
		t.Fatal("expected nil claim when no resident terminal runtimes")
	}
}

// TestIsResidentTerminalRuntimeLocked covers the helper function.
func TestIsResidentTerminalRuntimeLocked(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	// Running delegate with no live runtime: not resident terminal.
	agg := c.durable["dlg_target"]
	if c.isResidentTerminalRuntimeLocked("dlg_target", agg) {
		t.Fatal("expected false for running delegate without terminal state")
	}
	// Nonexistent delegate: not resident terminal.
	if c.isResidentTerminalRuntimeLocked("nonexistent", nil) {
		t.Fatal("expected false for nonexistent delegate")
	}
}

// TestResidentDelegateRuntime covers the residentDelegateRuntime method.
func TestResidentDelegateRuntime(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// No live runtime attached: returns nil.
	if got := c.residentDelegateRuntime("dlg_target"); got != nil {
		t.Fatalf("expected nil for delegate without runtime, got %v", got)
	}
	// Nonexistent: returns nil.
	if got := c.residentDelegateRuntime("nonexistent"); got != nil {
		t.Fatalf("expected nil for nonexistent delegate, got %v", got)
	}
}

// TestReconcileRequirements covers the ReconcileRequirements method.
func TestReconcileRequirements(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	// ReconcileRequirements should not panic for a seeded delegate.
	_ = c.ReconcileRequirements()
}

// TestReplayDeliveries covers the ReplayDeliveries method.
func TestReplayDeliveries(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	deliveries := c.ReplayDeliveries()
	// Should return empty slice for a delegate with no deliveries.
	if deliveries == nil {
		t.Fatal("expected non-nil slice")
	}
}
