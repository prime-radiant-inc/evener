package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A delivery acknowledged outside an inline receipt publishes a wake in its own
// right, and that wake is newer evidence than a settled user-stop quiet. The
// acknowledgment appends the wake-triggering events itself and notes the
// delivery as seen, so the arm that follows appends nothing: if the release
// journaling lived only in the arm, this path would publish a wake with no
// release, and an idle reclaim would rebuild the quiet over the row the
// delivery just freed. The release must be journaled by the acknowledgment's
// own append.
func TestNonInlineDeliveryAckJournalsTheHoldRelease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_hold", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_hold")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child" || plans[0].ownerDelegateID != "dlg_hold" {
		t.Fatalf("replayed delivery plans = %#v, want one report from dlg_child to dlg_hold", plans)
	}
	// The owner settles an attention-triggered stop. Its generation is the
	// newest quiet the child's report supersedes, and nothing has released it.
	journalDeriveHeldStopWithTrigger(t, c, delegatestore.TriggerAttention, delegatestore.OutcomeCancelled, "cancelled")
	c.mu.Lock()
	settled := c.latestAttentionStop["dlg_hold"]
	floor := c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if settled != 1 || floor != 0 {
		t.Fatalf("seeded stop = generation %d floor %d, want the stop settled at 1 with no release", settled, floor)
	}

	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	if _, err := c.CompleteDelivery(token, true); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}

	c.mu.Lock()
	released := c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if released != settled {
		t.Fatalf("the acknowledgment published a wake with no journaled release (floor %d, want %d): the quiet the delivery ended is rebuilt by the next idle reclaim, and the row it freed is held again", released, settled)
	}
}
