package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAFailedPublicationIsRetriedUntilItsReportIsArmed pins that an
// acknowledged report survives a failed publication.
//
// A stop's retry acknowledges its delivery, which removes it from the durable
// replay set, and then publishes the report into root history and arms its
// wake. If that publication fails, the acknowledgment is already durable and
// nothing in the controller's wake retry revisits the row: it scans delegate
// wake state, not acknowledged reports. Without a receiver-side obligation the
// report is stranded — outside history, outside the wake set — until a restart
// or a user action.
//
// The assertion is on the wake set, not on the retry having run: a retry that
// eventually succeeds would hide a stranding bug, and a retry that arms without
// publishing would recreate the consumed-but-never-delivered defect.
func TestAFailedPublicationIsRetriedUntilItsReportIsArmed(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, topLevel: true, realRoot: true})
	delivery := f.deliveries[0]
	attentionID := delegateAttentionID(delivery.deliveryID)
	content := `<delegate-notification delegate_id="dlg_target">report</delegate-notification>`

	if _, deposited, err := depositDelegateAttentionReport(f.root, attentionID, content); err != nil {
		t.Fatalf("deposit the root report: %v", err)
	} else if !deposited {
		t.Fatal("premise broken: the report was not recorded for the root receiver")
	}

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

	// The publication fails once, as a transient transcript read does.
	injected := errors.New("injected publication failure")
	f.root.mu.Lock()
	f.root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		return delegateAttentionFold{}, injected
	}
	f.root.mu.Unlock()
	if err := c.wakeDeliveryReceiver(wakes[0].receiver, wakes[0].attentionIDs, wakes[0].wakeGeneration, nil); !errors.Is(err, injected) {
		t.Fatalf("wake error = %v, want the injected publication failure", err)
	}
	f.root.mu.Lock()
	f.root.cfg.testOnly.delegateAttentionReadFold = nil
	f.root.mu.Unlock()

	// The receiver owes the report: it is not in history, and it is waiting on
	// the receiver's own retry surface rather than only the controller's.
	f.root.mu.Lock()
	inHistory := false
	for _, turn := range f.root.history {
		if turn.AttentionID == attentionID {
			inHistory = true
		}
	}
	f.root.mu.Unlock()
	if inHistory {
		t.Fatal("premise broken: the report reached history despite the failed publication")
	}
	f.root.attentionMu.Lock()
	_, owed := f.root.delegateAttentionUnpublishedReports[attentionID]
	_, retrying := f.root.delegateAttentionArmIDs[attentionID]
	f.root.attentionMu.Unlock()
	if !owed || !retrying {
		t.Fatalf("after a failed publication the report is owed=%v and on the arm retry=%v: both must hold, or nothing publishes it and the reported row is stranded outside history and outside the wake set", owed, retrying)
	}

	// The retry publishes before arming, so the report is in history and its ID
	// is armed by the time the attempt reports success.
	if err := f.root.armDelegateAttentionOnce(attentionID); err != nil {
		t.Fatalf("retry the arm: %v", err)
	}
	f.root.mu.Lock()
	published := 0
	for _, turn := range f.root.history {
		if turn.AttentionID == attentionID {
			published++
		}
	}
	f.root.mu.Unlock()
	if published != 1 {
		t.Fatalf("root history holds the report %d times after the retry, want 1: the retry must publish the acknowledged report before it arms it", published)
	}
	f.root.attentionMu.Lock()
	_, stillOwed := f.root.delegateAttentionUnpublishedReports[attentionID]
	f.root.attentionMu.Unlock()
	if stillOwed {
		t.Fatal("the receiver still owes a publication after a successful one: the obligation must clear once the turn is resident")
	}
	// The row is still pending, so this is about publication and arming rather
	// than about a resolution that already happened.
	f.root.attentionMu.Lock()
	pending := delegateAttentionPendingSet(f.root.delegateAttentionPendingIDsLocked())
	f.root.attentionMu.Unlock()
	if _, ok := pending[attentionID]; !ok {
		t.Fatalf("premise broken: %q is no longer pending, so this cannot tell publication from resolution", attentionID)
	}
	_ = delegatestore.OutcomeCompleted
}
