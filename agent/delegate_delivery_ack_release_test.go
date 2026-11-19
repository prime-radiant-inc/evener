package agent

import (
	"testing"
	"time"

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

// A wake can be admitted while the owner is already running the successor
// generation a user's stop triggered. That wake is newer evidence than the stop
// the generation before the run settled, so its release names that earlier
// generation. The open generation stays outside the release: a stop that
// settles it afterwards is newer than the delivery and keeps its own quiet.
func TestNonInlineDeliveryAckReleasesThroughThePrecedingSettledGeneration(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_hold", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_hold")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child" || plans[0].ownerDelegateID != "dlg_hold" {
		t.Fatalf("replayed delivery plans = %#v, want one report from dlg_child to dlg_hold", plans)
	}
	// Generation 1 is an attention stop whose quiet stands, and generation 2 is
	// the successor run the stop's own row started. It is still open when the
	// child's report is acknowledged.
	journalDeriveHeldStopWithTrigger(t, c, delegatestore.TriggerAttention, delegatestore.OutcomeCancelled, "cancelled")
	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateRunStarted,
		DelegateID: "dlg_hold",
		RunStarted: &delegatestore.RunStarted{
			Generation: 2,
			Trigger:    delegatestore.TriggerAttention,
			StartedAt:  time.Unix(30, 0).UTC(),
		},
	})
	open := c.durable["dlg_hold"].CurrentRunOpen
	settled := c.durable["dlg_hold"].Generation
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("open the successor run: %v", err)
	}
	if !open || settled != 2 {
		t.Fatalf("premise broken: generation %d open = %t, want generation 2 open", settled, open)
	}

	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	acked, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}

	c.mu.Lock()
	released := c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if released != 1 {
		t.Fatalf("the acknowledgment released generation %d while generation 2 was open, want the settled generation 1: the stop before the open run keeps its quiet and the release is lost", released)
	}
	if acked.ackWakeGeneration != 1 {
		t.Fatalf("the acknowledgment carried generation %d, want the settled generation 1 it released", acked.ackWakeGeneration)
	}

	// The successor run settles cancelled by the same attention trigger. It is a
	// newer stop than the delivery, so the delivery's release does not cover it.
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: []byte(`"stopped"`)}
	c.mu.Lock()
	_, err = c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_hold",
			RunFinished: &delegatestore.RunFinished{
				Generation:  2,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "cancelled", EndedAt: time.Unix(40, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_hold", 2),
				Packet:      &stopped,
			},
		},
	)
	released = c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("settle the successor run: %v", err)
	}
	if released >= 2 {
		t.Fatalf("the delivery's release reached generation %d: a stop that settles after the delivery is newer evidence, and its quiet must stand", released)
	}
}
