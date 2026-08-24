package agent

import (
	"testing"
)

// TestReconcileDelegateAttentionFromTranscripts_NilController covers the
// nil-controller guard (lines 64-65).
func TestReconcileDelegateAttentionFromTranscripts_NilController(t *testing.T) {
	var c *delegateTreeController
	err := c.reconcileDelegateAttentionFromTranscripts()
	if err == nil {
		t.Fatal("expected error for nil controller")
	}
}

// TestReconcileDelegateAttentionFromTranscripts_Empty covers the happy path
// with no eligible delegates (lines 70-76, 96-100).
func TestReconcileDelegateAttentionFromTranscripts_Empty(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	if err := c.reconcileDelegateAttentionFromTranscripts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDelegateAttentionProjectionEligible covers the pure helper function.
func TestDelegateAttentionProjectionEligible(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	c.mu.Lock()
	durable := c.durable
	c.mu.Unlock()
	// An idle delegate should be eligible.
	if !delegateAttentionProjectionEligible(durable, "dlg_idle") {
		t.Fatal("expected idle delegate to be eligible")
	}
	// Nonexistent delegate: not eligible.
	if delegateAttentionProjectionEligible(durable, "nonexistent") {
		t.Fatal("expected nonexistent delegate to NOT be eligible")
	}
}

// TestNextIdleDelegateAttention_NoPending covers the path where there are
// no pending attention IDs.
func TestNextIdleDelegateAttention_NoPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	_, _, pending := c.nextIdleDelegateAttention()
	if pending {
		t.Fatal("expected pending=false with no attention")
	}
}

// TestHasPendingDelegateAttention_NoPending covers the path where there are
// no pending attention IDs.
func TestHasPendingDelegateAttention_NoPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	if c.hasPendingDelegateAttention() {
		t.Fatal("expected false with no pending attention")
	}
}

// TestRetryDelegateAttentionLater covers the no-op retry path.
func TestRetryDelegateAttentionLater(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.retryDelegateAttentionLater()
	// Should not panic.
}

// TestPermanentlyFencedDelegateAttention covers the method with no fenced
// delegates.
func TestPermanentlyFencedDelegateAttention(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_idle", "")
	plans := c.permanentlyFencedDelegateAttention()
	if len(plans) != 0 {
		t.Fatalf("expected 0 fenced plans, got %d", len(plans))
	}
}

// TestReservedAttentionID covers the reservedAttentionID method with no
// reserved attention.
func TestReservedAttentionID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	s := &Session{}
	if got := c.reservedAttentionID(s); got != "" {
		t.Fatalf("expected empty reserved attention ID, got %q", got)
	}
}
