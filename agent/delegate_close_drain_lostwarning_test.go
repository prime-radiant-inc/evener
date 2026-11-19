package agent

import (
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A lost-result warning the pass already proved must survive a LATER permanent
// acknowledgment failure.
//
// The loop accumulates an abandoned inline receipt's warning, then can hit a
// permanent store error on a later receipt and return early. Returning an empty
// delegateMutationPlans there discarded the warning the pass had already
// discovered: the receipt that produced it is gone from the store, so no later
// pass can rediscover it, and the close drain had nothing to publish. A finding
// must not be thrown away by a subsequent error.
//
// The assertion is the typed warning's presence on the returned plans.
func TestStopPassKeepsALostResultWarningAcrossAPermanentAckFailure(t *testing.T) {
	// dlg_target carries the unreachable inline receipt that yields the warning.
	// dlg_other carries the retryable receipt whose acknowledgment fails
	// permanently. They are separate delegates because an acknowledgment is only
	// attempted for a delegate's HEAD pending delivery, so two receipts on one
	// delegate could never let the second reach the store.
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, otherPending: true})
	c := f.controller

	var failedID string
	c.mu.Lock()
	other := c.durable["dlg_other"]
	if other == nil || len(other.PendingDeliveries) == 0 {
		c.mu.Unlock()
		t.Fatal("fixture seeded no pending delivery for dlg_other")
	}
	c.nextToken++
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: other.PendingDeliveries[0].DeliveryID}
	failedID = token.deliveryID
	c.deliveries[token.processID] = &delegateDeliveryAdmission{
		token:           token,
		delegateID:      "dlg_other",
		callerCommitted: true,
		claim:           delegateDeliveryClaimToken(token),
		retryable:       true,
	}
	c.stop.deliveries[token] = struct{}{}
	c.mu.Unlock()

	permanent := fmt.Errorf("test: store latched unusable: %w", delegatestore.ErrStoreUnusable)
	var faultFired bool
	c.appendFault = func(appended []delegatestore.Event) error {
		if delegateEventsAcknowledgeDelivery(appended, failedID) {
			faultFired = true
			return permanent
		}
		return nil
	}

	c.mu.Lock()
	plans, _, err := c.acknowledgeRetryableStopDeliveriesLocked(c.stop)
	c.mu.Unlock()

	// The premise: the permanent failure really did short-circuit the pass, so
	// the assertion below is about the early return and nothing else.
	if !errors.Is(err, delegatestore.ErrStoreUnusable) {
		t.Fatalf("acknowledge error = %v, want the permanent unusable-store error", err)
	}
	if !faultFired {
		t.Fatal("the injected acknowledgment fault never fired: the pass did not reach the " +
			"permanent-failure branch, so this test exercised a different path than the one it names")
	}

	lost := 0
	for _, warning := range plans.warnings {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("lost-result warnings on the plans returned alongside the permanent error = %d, want 1: "+
			"a warning this pass already proved is gone from the store, so discarding it here means "+
			"no later pass can report it", lost)
	}
}
