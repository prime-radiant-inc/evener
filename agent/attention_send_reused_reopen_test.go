package agent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestASendReusedReopenStaysInTheSuccessorsOwedSet pins the rule that a reopen
// is eligible on its own evidence when the generation it claims was a send's,
// not a stop's.
//
// A busy attention start reopens a report and an explicit send then runs on the
// same generation. That send began the generation's run, so a reopen claiming it
// finds a RunStarted — the same shape a stop's settle leaves. But a send that is
// stopped before presentation writes its own RunStarted and its commit clears the
// controller's wake set, so the report has nowhere else to be presented from.
//
// Treating a send-reused generation as a stop's own quiet drops the reopen from
// the successor's owed set: the committed batch clears NeedsAttention, the wake
// set is replaced with the empty capture, and the report waits for unrelated
// work. The row is reachable through the successor when the generation it claims
// was a send's.
func TestASendReusedReopenStaysInTheSuccessorsOwedSet(t *testing.T) {
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	reopenedID := delegateAttentionReopenID("delegate:delivery-attention-1", 1)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	// Generation 1 was an explicit send that settled cancelled, not an attention
	// stop. A release therefore never names it, and none is journaled.
	c.mu.Lock()
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_target",
			RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerOwnerInput, StartedAt: time.Unix(10, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_target",
			RunFinished: &delegatestore.RunFinished{
				Generation:  1,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "stopped", EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_target", 1),
				Packet:      &stopped,
			},
		},
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("settle generation 1 as a stopped send: %v", err)
	}
	if _, err := runtime.appendDelegateNotificationDurably(reopenedID, "the report the send reopened"); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	// Premise: the reopen is pending, not admitted, and the floor has not reached
	// its generation — so only the send-reused rule can keep it.
	if floor := c.attentionReleaseFloor("dlg_target"); floor >= 1 {
		t.Fatalf("premise broken: the release floor is %d, so this case is a release's and not a send's", floor)
	}
	pending, err := runtime.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention: %v", err)
	}
	reopenedPending := false
	for _, id := range pending {
		if id == reopenedID {
			reopenedPending = true
		}
	}
	if !reopenedPending {
		t.Fatalf("premise broken: the reopened row is not pending (%v)", pending)
	}

	reservation, err := c.ReserveAttention(runtime, freshID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the fresh row: %v", err)
	}
	c.mu.Lock()
	var owed []string
	for _, record := range c.reservations {
		if record != nil && record.delegateID == "dlg_target" {
			owed = append(owed, record.attentionPendingIDs...)
		}
	}
	c.mu.Unlock()
	if slices.Contains(owed, reopenedID) {
		return
	}
	t.Fatalf("the successor's owed set is %v and the report a stopped send reopened (%q) is not reachable through it: the send started the generation it claims, so nothing else will present it.\nrelease floor=0 reopened=%q", owed, reopenedID, reopenedID)
}
