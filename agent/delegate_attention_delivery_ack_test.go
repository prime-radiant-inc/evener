package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestDeliveryAckClearsParkedOwnerRetryState pins the ack half of the queue's
// retry bookkeeping: a newly delivered report is new evidence, so the owner's
// parked flag and counted cold-restore failures must not outlive it. Without
// the reset, a cold receiver acknowledged after an earlier park would keep
// skipping its own fresh row.
func TestDeliveryAckClearsParkedOwnerRetryState(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	seedDelegateControllerDelivery(t, c, "dlg_target")

	c.mu.Lock()
	aggregate := c.durable["dlg_target"]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
		c.mu.Unlock()
		t.Fatal("fixture seeded no pending delivery for dlg_target")
	}
	deliveryID := aggregate.PendingDeliveries[0].DeliveryID
	c.nextToken++
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: deliveryID}
	c.deliveries[token.processID] = &delegateDeliveryAdmission{
		token:      token,
		delegateID: "dlg_target",
		ownerID:    "dlg_owner",
		claim:      delegateDeliveryClaimToken{processID: token.processID, deliveryID: deliveryID},
	}
	state := c.attentionStateLocked("dlg_owner")
	state.parked = true
	state.restoreFailures = 3
	c.mu.Unlock()

	if _, err := c.CompleteDelivery(token, true); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	state = c.attention["dlg_owner"]
	if state == nil {
		t.Fatal("delivery ack dropped the owner's attention bookkeeping instead of resetting it")
	}
	if state.parked || state.restoreFailures != 0 {
		t.Fatalf("owner attention state after ack = parked:%t failures:%d, want false/0", state.parked, state.restoreFailures)
	}
	owner := c.durable["dlg_owner"]
	if owner == nil || !owner.NeedsAttention || len(owner.PendingAttention) != 1 || !owner.PendingAttention[0].Committed {
		t.Fatalf("owner projection after ack = %+v, want one committed row raising attention", owner)
	}
	if owner.PendingAttention[0].AttentionID != delegateAttentionID(token.deliveryID) {
		t.Fatalf("queued attention id = %q, want the acknowledged delivery's", owner.PendingAttention[0].AttentionID)
	}
	if got := c.durable["dlg_target"]; got == nil || len(got.PendingDeliveries) != 0 {
		t.Fatalf("target pending deliveries after ack = %+v, want none", got)
	}
	if got := c.durable["dlg_owner"]; got == nil || got.Phase != delegatestore.PhaseIdle {
		t.Fatalf("owner phase after ack = %+v, want idle", got)
	}
}
