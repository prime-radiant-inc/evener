package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// The wake callback must release against the generation the acknowledgment
// journaled, not one it samples itself after the acknowledgment lands. A stop
// that settles in between is newer evidence than the delivery, and its quiet
// must survive the delivery's release. The sample is taken under the controller
// lock where the funnel journals the release, so it cannot move afterwards.
func TestDeliveryAckCarriesTheGenerationItJournaled(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_hold", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_hold")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child" || plans[0].ownerDelegateID != "dlg_hold" {
		t.Fatalf("replayed delivery plans = %#v, want one report from dlg_child to dlg_hold", plans)
	}
	// The owner rests at a settled generation whose quiet stands: the newest
	// evidence a wake published now supersedes.
	journalStopAtGeneration(t, c, "dlg_hold", 1)

	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	acked, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	if acked.ackWakeGeneration != 1 {
		t.Fatalf("the acknowledgment carried generation %d, want the generation its wake superseded (1)", acked.ackWakeGeneration)
	}

	// A user stop settles after the acknowledgment. The wake in hand predates
	// it, so the callback must still release against generation 1: sampling the
	// generation again at callback time would read 2 and clear a quiet newer
	// than the delivery, and the automatic drive would then present the row that
	// stop was holding out.
	journalStopAtGeneration(t, c, "dlg_hold", 2)
	c.mu.Lock()
	newer := c.attentionWakeGenerationLocked("dlg_hold")
	c.mu.Unlock()
	if newer != 2 {
		t.Fatalf("the newer stop settled at generation %d, want 2", newer)
	}
	if acked.ackWakeGeneration == newer {
		t.Fatalf("the carry moved with the newer stop (generation %d): the callback releases against evidence newer than the delivery it belongs to, and the user's stop is cleared by a delivery that predates it", newer)
	}
}

// journalStopAtGeneration settles a user stop at generation for delegateID,
// leaving the aggregate idle at that generation with no release journaled: the
// newest quiet the delegate's rows carry.
func journalStopAtGeneration(t *testing.T, c *delegateTreeController, delegateID string, generation uint64) {
	t.Helper()
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: []byte(`"stopped"`)}
	c.mu.Lock()
	defer c.mu.Unlock()
	settledAt := time.Unix(20+int64(generation), 0).UTC()
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: delegateID,
			RunStarted: &delegatestore.RunStarted{
				Generation: generation,
				Trigger:    delegatestore.TriggerAttention,
				StartedAt:  time.Unix(10+int64(generation), 0).UTC(),
			},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: delegateID,
			RunFinished: &delegatestore.RunFinished{
				Generation:  generation,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "cancelled", EndedAt: settledAt},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID(delegateID, generation),
				Packet:      &stopped,
			},
		},
	)
	if err != nil {
		t.Fatalf("journal the stop at generation %d: %v", generation, err)
	}
}
