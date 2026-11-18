package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestAnUnacknowledgedDepositIsJudgedByThePostingDelegatesQueue pins which
// delegate's queue answers the acknowledgment question, and which delegate the
// delivery has to be addressed to for that answer to apply.
//
// The journal queues a delivery on the delegate whose run posted the packet — a
// child's report sits in the child's aggregate — and records the receiver
// separately as PendingDelivery.OwnerDelegateID (fold.go:858-870). A receiver
// that searched its own queue therefore finds nothing and reads every
// outstanding deposit as acknowledged, which publishes a report whose release
// floor has not advanced. The identity is what names the queue; the delivery's
// owner is what names the receiver.
//
// Both halves are pinned. The child's queue holds the delivery and this session
// is its owner, so the report waits. A different receiver reading the same
// queued delivery must not have that delivery answer for it. Once the
// acknowledgment commits, the report stops waiting.
func TestAnUnacknowledgedDepositIsJudgedByThePostingDelegatesQueue(t *testing.T) {
	const (
		parentDelegateID = "dlg_parent"
		childDelegateID  = "dlg_child"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, parentDelegateID, "")
	seedDelegateControllerIdle(t, c, childDelegateID, parentDelegateID)
	seedDelegateControllerDelivery(t, c, childDelegateID)

	deliveryID := delegateDeliveryID(childDelegateID, 1)
	attentionID := delegateAttentionID(deliveryID)
	if queued := pendingDeliveryIDs(c, childDelegateID); len(queued) != 1 || queued[0] != deliveryID {
		t.Fatalf("premise broken: the child's queue holds %v, want only %q", queued, deliveryID)
	}

	receiver := &Session{id: "sess-receiver", delegateController: c, owningDelegateID: parentDelegateID}
	if !receiver.delegateAttentionReportAwaitsAcknowledgment(attentionID) {
		t.Fatalf("the deposit %q reads as acknowledged while the posting delegate's queue still holds it: the restore would publish a report whose acknowledgment has not landed, and the request built from it would carry the new report while withholding the older report that acknowledgment is going to release", attentionID)
	}

	// The delivery names this receiver as its owner. A session that is not the
	// owner must not have this queue entry answer for it, or any delegate
	// reading a sibling's queue would adopt a report it was never sent.
	stranger := &Session{id: "sess-stranger", delegateController: c, owningDelegateID: "dlg_stranger"}
	if stranger.delegateAttentionReportAwaitsAcknowledgment(attentionID) {
		t.Fatalf("a session that is not the delivery's owner had %q answer for it: the queue says which delegate posted the packet, and the delivery says whom it is for, so both must agree before a report waits", attentionID)
	}

	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID:           childDelegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: deliveryID},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge %q: %v", deliveryID, err)
	}
	if receiver.delegateAttentionReportAwaitsAcknowledgment(attentionID) {
		t.Fatalf("the acknowledged deposit %q still reads as unacknowledged: a restore would withhold a report whose release floor has already advanced", attentionID)
	}
}

// TestCompactionWithholdsADepositItsAcknowledgmentHasNotReleased pins that live
// compaction asks the same acknowledgment question the restore asks.
//
// A fold that drops an attention turn reads the pending set back from the
// transcript so the summary cannot swallow a report a request still owes. The
// rows it takes must be the rows a restore would take: a deposit whose
// acknowledgment has not committed is still deferred, and publishing it into the
// request would carry the new report while withholding the older report that
// same acknowledgment will release. Restore held such rows back; compaction
// restored every pending turn, so the two paths answered the same question
// differently and the request could present the pair in the inverted order.
func TestCompactionWithholdsADepositItsAcknowledgmentHasNotReleased(t *testing.T) {
	const (
		parentDelegateID = "dlg_parent"
		childDelegateID  = "dlg_compact_child"
		sessionID        = "sess-compact-receiver"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, parentDelegateID, "")
	seedDelegateControllerIdle(t, c, childDelegateID, parentDelegateID)
	seedDelegateControllerDelivery(t, c, childDelegateID)

	deliveryID := delegateDeliveryID(childDelegateID, 1)
	attentionID := delegateAttentionID(deliveryID)
	path := transcriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	deposit := schema.NewTurn(schema.TurnSteering, llm.User("a delegate report whose acknowledgment is outstanding"))
	deposit.AttentionID = attentionID
	if err := writer.AppendDurable(deposit); err != nil {
		t.Fatalf("append the deposit: %v", err)
	}

	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c, owningDelegateID: parentDelegateID}
	runtime.attachTranscript(writer, nil)

	runtime.attentionMu.Lock()
	pending, err := runtime.pendingAttentionTurnsLocked()
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("read the pending turns: %v", err)
	}
	if turnsCarryAttentionID(pending, attentionID) {
		t.Fatalf("compaction retained the deposit %q while its acknowledgment is still outstanding: the request rebuilt from it carries the new report and withholds the older report that same acknowledgment is going to release, which is the resident order inverted", attentionID)
	}
	runtime.attentionMu.Lock()
	err = runtime.retainPendingDelegateAttentionTurnsLocked(delegateAttentionFold{})
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("restore the pending turns: %v", err)
	}
	runtime.mu.Lock()
	residentDeposit := false
	for _, turn := range runtime.history {
		if turn.AttentionID == attentionID {
			residentDeposit = true
		}
	}
	runtime.mu.Unlock()
	if residentDeposit {
		t.Fatalf("the restore published the deposit %q whose acknowledgment is still outstanding", attentionID)
	}

	c.mu.Lock()
	_, err = c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID:           childDelegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: deliveryID},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge %q: %v", deliveryID, err)
	}
	runtime.attentionMu.Lock()
	pending, err = runtime.pendingAttentionTurnsLocked()
	runtime.attentionMu.Unlock()
	if err != nil {
		t.Fatalf("read the pending turns after the acknowledgment: %v", err)
	}
	if !turnsCarryAttentionID(pending, attentionID) {
		t.Fatalf("compaction still withheld the acknowledged deposit %q: a report whose release floor has advanced must be restorable", attentionID)
	}
}

// turnsCarryAttentionID reports whether any of turns carries attentionID.
func turnsCarryAttentionID(turns []schema.Turn, attentionID string) bool {
	for _, turn := range turns {
		if turn.AttentionID == attentionID {
			return true
		}
	}
	return false
}
