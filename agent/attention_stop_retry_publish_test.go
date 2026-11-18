package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAStopRetriedAcknowledgmentPublishesTheReportBeforeArming pins that the
// retried acknowledgment publishes the report it acknowledges.
//
// A child's report is recorded before its acknowledgment and deliberately kept
// out of resident history until that acknowledgment commits, because the
// acknowledgment is what raises the release floor the report is presented
// against. If the first acknowledgment fails, the stop retries it — and that
// retry holds only the report ID.
//
// An arming that skipped the publication would leave a durable report that no
// request could carry: the root's notification turn selects the armed ID, marks
// it consumed, and the model never sees the report. The assertion is on the
// root's history at the moment the wake returns, which is the state the first
// request after arming would project.
func TestAStopRetriedAcknowledgmentPublishesTheReportBeforeArming(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, topLevel: true, realRoot: true})
	delivery := f.deliveries[0]
	attentionID := delegateAttentionID(delivery.deliveryID)
	content := `<delegate-notification delegate_id="dlg_target">report</delegate-notification>`

	// The deposit: durable in the root's transcript, withheld from its history,
	// exactly as a delivery's first acknowledgment attempt leaves it.
	_, deposited, err := depositDelegateAttentionReport(f.root, attentionID, content)
	if err != nil {
		t.Fatalf("deposit the root report: %v", err)
	}
	if !deposited {
		t.Fatalf("premise broken: the report was not recorded for the root receiver")
	}
	f.root.mu.Lock()
	inHistory := false
	for _, turn := range f.root.history {
		if turn.AttentionID == attentionID {
			inHistory = true
		}
	}
	f.root.mu.Unlock()
	if inHistory {
		t.Fatalf("premise broken: the deposited report is already in resident history, so this cannot tell a publishing wake from one that skips it")
	}

	// The stop retries the acknowledgment itself and wakes with the ID alone.
	c := f.controller
	c.mu.Lock()
	_, wakes, err := c.acknowledgeRetryableStopDeliveriesLocked(c.stop)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge retryable stop deliveries: %v", err)
	}
	if len(wakes) != 1 {
		t.Fatalf("wakes = %d, want one for the root receiver", len(wakes))
	}
	if err := c.wakeDeliveryReceiver(wakes[0].receiver, wakes[0].attentionIDs, wakes[0].wakeGeneration, nil); err != nil {
		t.Fatalf("wake the root receiver: %v", err)
	}

	f.root.mu.Lock()
	published := false
	for _, turn := range f.root.history {
		if turn.AttentionID == attentionID {
			published = true
		}
	}
	f.root.mu.Unlock()
	if !published {
		t.Fatalf("the retried acknowledgment armed %q without publishing its report into root history: a notification turn that selects that ID marks a report consumed that no request ever carried", attentionID)
	}

	// The retry can run again after a partial success; the publication is
	// ID-keyed, so it must not duplicate the turn.
	if err := c.wakeDeliveryReceiver(wakes[0].receiver, wakes[0].attentionIDs, wakes[0].wakeGeneration, nil); err != nil {
		t.Fatalf("repeat the wake: %v", err)
	}
	f.root.mu.Lock()
	copies := 0
	for _, turn := range f.root.history {
		if turn.AttentionID == attentionID {
			copies++
		}
	}
	f.root.mu.Unlock()
	if copies != 1 {
		t.Fatalf("root history holds %d copies of %q after a repeated wake, want 1: publication must be idempotent by attention ID", copies, attentionID)
	}
	// The row is still pending, so the assertion above is about publication and
	// not about a resolution that already happened.
	f.root.attentionMu.Lock()
	pending := delegateAttentionPendingSet(f.root.delegateAttentionPendingIDsLocked())
	_, stillPending := pending[attentionID]
	f.root.attentionMu.Unlock()
	if !stillPending {
		t.Fatalf("premise broken: %q is no longer pending, so this test cannot tell publication from resolution", attentionID)
	}
	_ = delegatestore.OutcomeCompleted
}
