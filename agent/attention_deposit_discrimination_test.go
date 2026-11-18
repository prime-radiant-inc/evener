package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestTheDelegateJournalDiscriminatesADeferredDepositFromAnAcknowledgedRow pins
// which durable record decides whether a delivery's report may be published.
//
// A delivery records its report before committing the acknowledgment and
// publishes it after, so any restore that folds the transcript meets both
// classes of row: a deposit whose acknowledgment has not landed, and a row whose
// delivery is already acknowledged. The two must not be published alike — the
// first would reach a request whose release floor has not advanced — so the
// restore needs a record that separates them.
//
// The transcript's own delivery-commit table cannot: it records the tool-results
// turn a resident reply carried (session_attention.go:310-345), and a stop's
// retry acknowledges inside the journal without ever writing one
// (delegate_delivery.go:470-484). Both classes therefore read as "no commit",
// which is what an earlier restore gate mistook for "still deferred".
//
// The record that does separate them is the journal aggregate's pending-delivery
// queue — delegatestore.PendingDelivery at record.go:221-226, appended when the
// terminal packet is prepared (fold.go:864) and popped by the acknowledgment
// (fold.go:575-588), read through delegatePendingLocked at
// delegate_delivery.go:584-589. A delivery still in that queue is an
// unacknowledged deposit; a delivery absent from it has been acknowledged,
// whichever side committed it.
//
// The two rows below differ in exactly that one respect and in nothing else:
// same delegate, same journal, same transcript, no commit turn for either.
func TestTheDelegateJournalDiscriminatesADeferredDepositFromAnAcknowledgedRow(t *testing.T) {
	t.Parallel()
	const (
		delegateID = "dlg_discriminate"
		sessionID  = "child-dlg_discriminate"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, delegateID, "")

	// Two real deliveries for one delegate: the first is acknowledged, the
	// second is still the queue's head.
	seedDelegateControllerDelivery(t, c, delegateID)
	firstID := delegateDeliveryID(delegateID, 1)
	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID:           delegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: firstID},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge %q: %v", firstID, err)
	}
	seedDelegateControllerDelivery(t, c, delegateID)
	secondID := delegateDeliveryID(delegateID, 2)

	// Premise: the journal holds exactly the second delivery, so the two rows
	// differ in acknowledgment state alone.
	queued := pendingDeliveryIDs(c, delegateID)
	if len(queued) != 1 || queued[0] != secondID {
		t.Fatalf("premise broken: the journal queues %v, want only %q", queued, secondID)
	}

	// Both rows are durably recorded in the transcript, and no tool-results turn
	// committed either one: this is the state a restore actually meets.
	path := transcriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for _, deliveryID := range []string{firstID, secondID} {
		turn := schema.NewTurn(schema.TurnSteering, llm.User("a delegate report for "+deliveryID))
		turn.AttentionID = delegateAttentionID(deliveryID)
		if err := writer.AppendDurable(turn); err != nil {
			t.Fatalf("append %q: %v", deliveryID, err)
		}
	}
	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c}
	runtime.attachTranscript(writer, nil)
	runtime.attentionMu.Lock()
	fold, err := runtime.delegateAttentionFoldLocked()
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("fold the transcript: %v", err)
	}

	// The transcript table answers the same for both: it cannot discriminate.
	for _, deliveryID := range []string{firstID, secondID} {
		if commit := fold.deliveryCommits[deliveryID]; commit != "" {
			t.Fatalf("premise broken: %q has a transcript commit record (%q), so this case does not exercise a journal-only acknowledgment", deliveryID, commit)
		}
	}

	// The journal's queue answers differently for each, which is the whole rule.
	if c.deliveryPending(delegateID, firstID) {
		t.Fatalf("the acknowledged delivery %q still reads as pending: a restore would withhold a report whose release floor has already advanced", firstID)
	}
	if !c.deliveryPending(delegateID, secondID) {
		t.Fatalf("the deposited delivery %q reads as acknowledged: a restore would publish a report whose acknowledgment has not landed, and the request built from it would carry a report whose release floor has not advanced", secondID)
	}
}
