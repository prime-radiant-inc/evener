package agent

import (
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestWatchSendDeliveryConcurrentExecutorsClaimOnce drives two executors of the
// SAME watch-send delivery id through the exact window issue #327 describes: the
// ancestor drain and the child turn-boundary flush can both reach
// deliverPendingWatchSend for one delivery id. Executor A is parked after its
// durable Delivered event lands but before it removes the in-memory pending and
// releases its receipt, so executor B enters and observes that same in-memory
// pending as current. Without a per-delivery executor claim, B re-appends the
// old pending behind Delivered and raises the "stable watch delivery is not the
// durable pending head" hard error, which the drain path escalates to exit 1.
// With the claim, B stands down before it touches the store.
func TestWatchSendDeliveryConcurrentExecutorsClaimOnce(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "concurrent delivery"})
	cfg := fixture.onlyWatchConfig(t)
	state := fixture.requireOnePending(t).state

	deliveredAppended := make(chan struct{}, 1)
	releaseA := make(chan struct{})
	var pendingAppends atomic.Int32
	originalAppend := fixture.sourceJM.appendEvent
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if exactWatchSendEvent(event, jobstore.EventWatchSendPending, state) {
			pendingAppends.Add(1)
		}
		err := originalAppend(event)
		if err == nil && exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, state) {
			deliveredAppended <- struct{}{}
			<-releaseA
		}
		return err
	}
	released := false
	defer func() {
		if !released {
			close(releaseA)
		}
		fixture.sourceJM.appendEvent = originalAppend
	}()

	aDone := make(chan error, 1)
	go func() {
		_, err := fixture.sourceJM.deliverPendingWatchSend(cfg, state, true)
		aDone <- err
	}()
	<-deliveredAppended

	// A's Delivered event is durable but A is still parked before removing the
	// in-memory pending, so B observes the same current pending. Reset the append
	// counter so it isolates only what B appends.
	pendingAppends.Store(0)
	deliveredB, errB := fixture.sourceJM.deliverPendingWatchSend(cfg, state, true)
	if errB != nil {
		close(releaseA)
		released = true
		<-aDone
		t.Fatalf("second deliverer error = %v, want stand-down without error", errB)
	}
	if deliveredB {
		close(releaseA)
		released = true
		<-aDone
		t.Fatal("second deliverer reported a delivery")
	}
	if got := pendingAppends.Load(); got != 0 {
		close(releaseA)
		released = true
		<-aDone
		t.Fatalf("second deliverer appended %d pending events, want 0", got)
	}

	close(releaseA)
	released = true
	if err := <-aDone; err != nil {
		t.Fatalf("first deliverer: %v", err)
	}

	eventsLog := loadJobStoreEvents(t, fixture.sourceJM)
	if got := countExactWatchSendEvents(eventsLog, jobstore.EventWatchSendDelivered, state); got != 1 {
		t.Fatalf("durable delivered events = %d, want exactly 1", got)
	}
	if receipt := fixture.sourceJM.stableWatchReceipt(state.DeliveryID); receipt != nil {
		t.Fatal("delivery receipt held after the first deliverer settled")
	}
}

// TestWatchSendDeliverySettledRerunStandsDown isolates the durable-head belt and
// braces independently of the executor claim. The claim is free (no executor is
// running), but the delivery has already settled durably; a rerun that still sees
// the in-memory pending must consult the delivered set and stand down instead of
// raising the hard "not the durable pending head" error.
func TestWatchSendDeliverySettledRerunStandsDown(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "settled rerun"})
	cfg := fixture.onlyWatchConfig(t)
	state := fixture.requireOnePending(t).state

	if err := fixture.sourceJM.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatalf("settle delivery: %v", err)
	}
	// Re-materialize the in-memory pending so the rerun passes the entry read
	// while the durable pending head is already gone.
	fixture.sourceJM.rememberUnpersistedTerminalPendingWatchSend(cfg, state)

	delivered, err := fixture.sourceJM.deliverPendingWatchSend(cfg, state, false)
	if err != nil {
		t.Fatalf("settled delivery rerun error = %v, want stand-down", err)
	}
	if delivered {
		t.Fatal("settled delivery rerun reported a delivery")
	}
	if receipt := fixture.sourceJM.stableWatchReceipt(state.DeliveryID); receipt != nil {
		t.Fatal("settled delivery rerun leaked a delivery receipt")
	}
}
