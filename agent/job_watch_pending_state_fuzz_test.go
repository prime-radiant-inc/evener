package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// FuzzWatchPendingStateProgram keeps the generic pending-state target on the
// registered stable watch path after retirement of loose delegate receivers.
func FuzzWatchPendingStateProgram(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Fuzz(func(t *testing.T, variant byte) {
		fixture := newStableWatchRuntimeFixture(t, nil)
		onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{
			Message: fmt.Sprintf("pending-%d", variant),
		})

		produced := producerSourceEvents(t, fixture.sourceJM, jobstore.EventWatchSendPending)
		if len(produced) != 1 {
			t.Fatalf("stable produced deliveries = %d, want 1", len(produced))
		}
		state := *produced[0].WatchSend
		if !state.StableReceiver || state.SourceDelegateID != "dlg_source" || state.SourceDelegateGeneration != 1 {
			t.Fatalf("stable pending identity = %#v", state)
		}
		if len(fixture.sourceJM.pendingWatchSendDeliveries(nil)) != 0 || len(producerSourceEvents(t, fixture.sourceJM, jobstore.EventWatchSendDelivered)) != 1 {
			t.Fatal("immediate canonical custody must settle the source once")
		}
		id := stableWatchAttentionID(state)
		if countAttentionEntries(t, fixture.rootTranscriptPath, id) != 1 {
			t.Fatal("source settlement lost or duplicated receiver custody")
		}
		assertNotificationPending(t, fixture.root, []string{id})
	})
}
