package agent

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// The refusal warning is durable and names the predicate that held the wake
// (#6330): a seeded lane — its generation finished durably while the live
// binding stayed latched, a report delivery owed — is refused by the binding
// guard, and the root's attention drive pass must say so on the warning's
// structured field, not silently. Before the fix no warning existed at all:
// the drive skipped what it could not start and a stranded lane was
// indistinguishable from a quiet one. Seeding the state directly, rather than
// racing a live drain for the hold window, keeps the pinned shape
// deterministic. "binding" is the wire token the predicate field carries
// (events.WarningData.RefusedPredicate), so it is asserted as a literal.
func TestDelegateAttentionDriveRefusalNamesTheHoldingPredicate(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerRunning(t, c, "dlg_lane", "")
	// Finish the generation durably but leave the live binding latched: the
	// strand shape — the lane owes a delivery, nothing may start for it, and
	// no runtime is presenting the row.
	c.mu.Lock()
	_, err := c.appendLocked(delegateRunFinishedEvent(
		delegateLease{delegateID: "dlg_lane", generation: 1},
		delegatestore.OutcomeCancelled, delegatestore.DispositionTerminalError, "seeded finish",
		time.Unix(20, 0).UTC(), delegateDeliveryID("dlg_lane", 1),
		&delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"seeded terminal error"`)},
	))
	if err == nil {
		c.noteDelegateAttentionLockedForTest("dlg_lane", delegateAttentionID(delegateDeliveryID("dlg_lane", 2)))
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed the lane's finished generation: %v", err)
	}
	client := llm.NewClient()
	root, err := NewSession(client, testOpenAICompatProfile("refusal-drive-root", "root-model", 0), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	root.delegateController = c
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	t.Cleanup(root.Close)
	eventsDone := captureSessionEvents(root)
	// Two passes sample the same refused lane: the episode warns exactly once.
	root.drivePendingDelegateAttention()
	root.drivePendingDelegateAttention()
	// The latched binding is the stranded state under test; release it before
	// the root closes so the tree's shutdown drain does not wait out a zombie
	// generation the test deliberately left behind.
	c.mu.Lock()
	delete(c.live, "dlg_lane")
	c.mu.Unlock()
	root.Close()
	var refusals []events.WarningData
	for _, warning := range warningEvents(<-eventsDone) {
		if warning.Code == events.WarningCodeDelegateAttentionDriveRefused {
			refusals = append(refusals, warning)
		}
	}
	if len(refusals) != 1 {
		t.Fatalf("refusal warnings = %d, want exactly one across two drive passes: %+v", len(refusals), refusals)
	}
	if refusals[0].DelegateID != "dlg_lane" {
		t.Fatalf("refusal warning named delegate %q, want dlg_lane", refusals[0].DelegateID)
	}
	if refusals[0].RefusedPredicate != "binding" {
		t.Fatalf("refusal predicate = %q, want the binding token", refusals[0].RefusedPredicate)
	}
}

// A delivery committed to a non-root receiver wakes the receiver in its own
// right AND arms the root's paced attention retry (#6330): the receiver's own
// wake cannot start its consumption generation — that drive is gated to the
// root — so the delivery must leave a durable root-side retry obligation, not
// just the one-shot poke. The receiver here cannot arm (its controller
// identity is incomplete, as after a partial restore), which is exactly the
// case whose one-shot wake used to strand a parked lane whenever the root's
// single pass was refused.
func TestDelegateAttentionDeliveryArmsTheRootRetryForANonRootReceiver(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	client := llm.NewClient()
	root, err := NewSession(client, testOpenAICompatProfile("delivery-retry-root", "root-model", 0), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	root.delegateController = c
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	t.Cleanup(func() {
		root.resetDelegateAttentionRetry()
		root.Close()
	})
	// A receiver with no controller identity: the arm refuses, and the
	// committed delivery's own wake must still reach it exactly once.
	var notifies atomic.Int32
	receiver := &Session{id: "lane-receiver", delegateController: c, notifyFunc: func() { notifies.Add(1) }}
	t.Cleanup(func() {
		// The refused arm left its paced re-arm behind on a receiver with no
		// runtime; retire the episode so the retry stops with the test.
		receiver.attentionMu.Lock()
		receiver.delegateAttentionArmIDs = nil
		receiver.resetDelegateAttentionArmRetryLocked()
		receiver.attentionMu.Unlock()
	})
	attentionID := delegateAttentionID(delegateDeliveryID("dlg_lane", 1))
	if err := c.wakeDeliveryReceiverAt(receiver, []string{attentionID}); err != nil {
		t.Fatalf("wake the non-root receiver: %v", err)
	}
	if got := notifies.Load(); got != 1 {
		t.Fatalf("receiver wake count = %d, want 1: the committed delivery wakes the receiver in its own right", got)
	}
	root.attentionMu.Lock()
	armed := root.stableAttentionRetry.active
	root.attentionMu.Unlock()
	if !armed {
		t.Fatal("the delivery left the root's attention retry unarmed: a refused or lost wake now strands the lane with no durable retry")
	}
}
