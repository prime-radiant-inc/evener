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

// A delivery's claim belongs to the generation it was admitted in. A stop that
// settles between the admission and its acknowledgment is newer evidence than
// the report, so the acknowledgment's release must not cover it: a reader of
// the journal would otherwise treat the report as delivered in the run that
// stop ended.
func TestDeliveryAckReleasesThroughTheGenerationItsClaimWasAdmittedIn(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_hold", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_hold")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child" || plans[0].ownerDelegateID != "dlg_hold" {
		t.Fatalf("replayed delivery plans = %#v, want one report from dlg_child to dlg_hold", plans)
	}
	// The owner is idle at generation 0 when the delivery is admitted.
	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}

	// The stop settles after the admission and before the acknowledgment
	// publishes anything. It is newer evidence than the delivery, so its quiet
	// must stand.
	journalStopAtGeneration(t, c, "dlg_hold", 1)

	acked, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}

	c.mu.Lock()
	released := c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if released != 0 {
		t.Fatalf("the acknowledgment released generation %d, want 0: the delivery was admitted before generation 1 settled, so the stop that settled it is newer evidence and a reader treats the report as delivered in the run that stop ended", released)
	}
	if acked.ackWakeGeneration != 0 {
		t.Fatalf("the acknowledgment carried generation %d, want the admission generation 0: a sample taken at acknowledgment time supersedes the stop that settled after the delivery", acked.ackWakeGeneration)
	}
}

// A carried admission sample can trail the owner's latest settled generation
// when the queued report starts a successor run and a stop settles it before
// the acknowledgment lands. The journal must still record the release the
// admission named: refusing it rejects the acknowledgment's whole batch, and
// the stop drain retries the same receipt forever. The newer stop's quiet must
// stand, which the monotonic record already guarantees.
func TestDeliveryAckRecordsAnAdmissionSampleBehindTheSettledGeneration(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_hold", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_hold")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child" || plans[0].ownerDelegateID != "dlg_hold" {
		t.Fatalf("replayed delivery plans = %#v, want one report from dlg_child to dlg_hold", plans)
	}
	// Generation 1 is an attention stop whose quiet stands. Generation 2 is the
	// successor run the stop's own queued row started, and it is still open when
	// the report is admitted.
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
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("open the successor run: %v", err)
	}

	token, admitted, err := c.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}

	// The subtree stop settles the successor before the acknowledgment lands.
	// It is not an attention stop of its own, so the earlier generation 1 stop
	// stays the newest attention quiet: that is the one the report's release
	// names, and it is now behind the settled generation.
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: []byte(`"stopped"`)}
	c.mu.Lock()
	_, err = c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_hold",
			RunFinished: &delegatestore.RunFinished{
				Generation:  2,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeStopped, Reason: delegatestore.ReasonStoppedByParent, EndedAt: time.Unix(40, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_hold", 2),
				Packet:      &stopped,
			},
		},
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("settle the successor run: %v", err)
	}

	acked, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery: %v: the acknowledgment's release names its admission generation, and a successor stop settling after it must not reject the batch", err)
	}

	c.mu.Lock()
	released := c.durable["dlg_hold"].AttentionHoldReleasedGeneration
	settled := c.durable["dlg_hold"].SettledGeneration()
	_, tracked := c.deliveries[token.processID]
	pending := len(c.durable["dlg_child"].PendingDeliveries)
	c.mu.Unlock()
	if settled != 2 {
		t.Fatalf("premise broken: settled generation %d, want 2", settled)
	}
	if tracked || pending != 0 {
		t.Fatalf("the acknowledgment did not land (tracked %t, %d pending): the stop drain retries the same receipt forever", tracked, pending)
	}
	if released != 1 {
		t.Fatalf("the acknowledgment recorded release generation %d, want 1: the report superseded the generation 1 stop it was admitted after", released)
	}
	if released >= settled {
		t.Fatalf("the acknowledgment's release reached generation %d: the stop that settled after the delivery is newer evidence, and its quiet must stand", released)
	}
	if acked.ackWakeGeneration != 1 {
		t.Fatalf("the acknowledgment carried generation %d, want the admission generation 1", acked.ackWakeGeneration)
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
