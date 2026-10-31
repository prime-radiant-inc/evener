package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// Missing paced source transport must strand this one-shot after the second
// failure. No new event, input, boundary drain or manual pump drives recovery.
func TestWatchOneShotPacedTransportRecovery(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"receiver sync", "source acknowledgement"} {
		t.Run(boundary, func(t *testing.T) {
			fs := &notificationFaultFS{Fs: afero.NewOsFs()}
			s, path := notificationReceiverForTest(t, fs)
			jm, err := newJobManager(s.stateDir, s.ID(), nil)
			if err != nil {
				t.Fatal(err)
			}
			jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
			t.Cleanup(func() { _ = jm.close() })
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, s.delegateController
			key, cfg := producerTimer(t, s, false)
			clk := jm.clock.(*agenttest.FakeClock)
			fault := errors.New("controlled watch storage failure")
			appendEvent := jm.appendEvent
			if boundary == "receiver sync" {
				fs.syncErr = fault
			} else {
				jm.appendEvent = func(event jobstore.Event) error {
					if event.Kind == jobstore.EventWatchSendDelivered {
						return fault
					}
					return appendEvent(event)
				}
			}
			jm.fireProgressTick(key, cfg)
			produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(produced) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
				t.Fatal("fault must leave one produced, unsettled one-shot")
			}
			id := stableWatchAttentionID(*produced[0].WatchSend)
			for _, delay := range []time.Duration{250 * time.Millisecond, 500 * time.Millisecond} {
				clk.Advance(delay)
				clk.Drain()
				if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
					t.Fatal("failed storage acknowledged source custody")
				}
			}
			fs.syncErr = nil
			jm.appendEvent = appendEvent
			// The next retry is due at one second, not an immediate busy loop.
			clk.Advance(time.Second - time.Nanosecond)
			clk.Drain()
			if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
				t.Fatal("transport retried before its paced deadline")
			}
			clk.Advance(time.Nanosecond)
			clk.Drain()
			if got := len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)); got != 1 {
				t.Fatalf("one-shot source acknowledgements after storage recovery = %d, want 1 without a new event", got)
			}
			if countAttentionEntries(t, path, id) != 1 || jm.hasPendingWatchSends() {
				t.Fatal("recovery must transfer one immutable frame and settle source debt")
			}
			assertNotificationPending(t, s, []string{id})
			clk.Advance(time.Minute)
			clk.Drain()
			if countAttentionEntries(t, path, id) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 {
				t.Fatal("retry after recovery duplicated custody or source accounting")
			}
		})
	}
}

// A successfully checked absence permits retirement only while holding the
// exact delivery claim. The latest coalesced body must still reach its receiver.
func TestWatchSupersededRetiresProvenUnaccepted(t *testing.T) {
	t.Parallel()
	fixture := newStableWatchRuntimeFixture(t, nil)
	for range 12 {
		fixture.persistFrameBeforeTransport(t, "replacement-sentinel")
	}
	fold, err := fixture.sourceJM.store.LoadWatchSends()
	if err != nil || len(fold.Superseded) != 11 {
		t.Fatalf("before retirement superseded=%d err=%v", len(fold.Superseded), err)
	}
	var claimed jobstore.WatchSendState
	for _, state := range fold.Superseded {
		claimed = *state
		break
	}
	jm := fixture.sourceJM
	if !jm.claimWatchSendDelivery(claimed.DeliveryID) {
		t.Fatal("could not hold predecessor delivery claim")
	}
	defer jm.releaseWatchSendDeliveryClaim(claimed.DeliveryID)
	if err := jm.settleSupersededWatchSends(fold.Superseded); err != nil {
		t.Fatal(err)
	}
	after, err := jm.store.LoadWatchSends()
	if err != nil || len(after.Superseded) != 1 || after.Superseded[claimed.DeliveryID] == nil {
		t.Fatalf("retirement must keep only the claimed predecessor, superseded=%d err=%v", len(after.Superseded), err)
	}
	jm.releaseWatchSendDeliveryClaim(claimed.DeliveryID)
	if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
		t.Fatal(err)
	}
	after, err = jm.store.LoadWatchSends()
	if err != nil || len(after.Superseded) != 0 || len(after.Pending) != 0 {
		t.Fatalf("after delivery pending=%d superseded=%d err=%v", len(after.Pending), len(after.Superseded), err)
	}
	if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 11 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 || producerRecordCount(t, fixture.root) != 1 {
		t.Fatal("coalescing must retire 11 absent bodies and accept only the surviving body")
	}
}

func TestWatchSupersededRetainsUncertainAndAcceptedCustody(t *testing.T) {
	t.Parallel()
	fs := &notificationFaultFS{Fs: afero.NewOsFs()}
	fixture := newStableWatchRuntimeFixture(t, fs)
	fixture.persistFrameBeforeTransport(t, "accepted-predecessor")
	old := fixture.requireOnePending(t).state
	if err := fixture.sourceJM.acceptWatchFrame(old); err != nil {
		t.Fatal(err)
	}
	fixture.persistFrameBeforeTransport(t, "retained-successor")
	latest := fixture.requireOnePending(t).state
	fs.syncErr = errors.New("controlled custody query failure")
	fold, err := fixture.sourceJM.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.sourceJM.settleSupersededWatchSends(fold.Superseded); !errors.Is(err, fs.syncErr) {
		t.Fatalf("custody uncertainty = %v", err)
	}
	fold, err = fixture.sourceJM.store.LoadWatchSends()
	if err != nil || len(fold.Superseded) != 1 || len(producerSourceEvents(t, fixture.sourceJM, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("uncertain accepted predecessor was retired")
	}
	fs.syncErr = nil
	if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM); err != nil {
		t.Fatal(err)
	}
	if countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(old)) != 1 || countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(latest)) != 1 {
		t.Fatal("accepted predecessor or successor content was lost or duplicated")
	}
	if len(producerSourceEvents(t, fixture.sourceJM, jobstore.EventWatchSendDelivered)) != 2 || len(producerSourceEvents(t, fixture.sourceJM, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("accepted predecessor must settle as delivered, never dropped")
	}
}
