package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestDelegateConversationSnapshotPreservesPendingDeliveries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		deliveries []delegatestore.PendingDelivery
	}{
		{name: "nil"},
		{name: "empty", deliveries: []delegatestore.PendingDelivery{}},
		{name: "populated", deliveries: []delegatestore.PendingDelivery{{
			DeliveryID: "delivery-1",
			Generation: 1,
			Packet: delegatestore.TerminalPacket{
				Kind:    delegatestore.PacketReported,
				Message: json.RawMessage(`"a"`),
			},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := delegatestore.State{"dlg_snapshot": {
				DelegateID:        "dlg_snapshot",
				Generation:        1,
				Phase:             delegatestore.PhaseRunning,
				PendingDeliveries: tc.deliveries,
			}}
			snapshot := cloneDelegateControllerState(t, state)
			if !reflect.DeepEqual(snapshot, state) {
				t.Fatalf("snapshot changed delivery representation: got %#v, want %#v", snapshot["dlg_snapshot"].PendingDeliveries, tc.deliveries)
			}

			// The snapshot must still detect real shape and nested-content changes.
			switch tc.name {
			case "nil":
				state["dlg_snapshot"].PendingDeliveries = []delegatestore.PendingDelivery{}
			case "empty":
				state["dlg_snapshot"].PendingDeliveries = nil
			case "populated":
				state["dlg_snapshot"].PendingDeliveries[0].Packet.Message[1] = 'b'
				if string(snapshot["dlg_snapshot"].PendingDeliveries[0].Packet.Message) != `"a"` {
					t.Fatal("source packet mutation changed the snapshot")
				}
			}
			if reflect.DeepEqual(snapshot, state) {
				t.Fatal("snapshot failed to detect a delivery mutation")
			}
		})
	}
}
