package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/events"
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

		pending := fixture.sourceJM.pendingWatchSendDeliveries(nil)
		if len(pending) != 1 {
			t.Fatalf("stable pending deliveries = %d, want 1", len(pending))
		}
		state := pending[0].state
		if !state.StableReceiver || state.SourceDelegateID != "dlg_source" || state.SourceDelegateGeneration != 1 {
			t.Fatalf("stable pending identity = %#v", state)
		}
	})
}
