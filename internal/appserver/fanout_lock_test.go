package appserver

import (
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

// TestFanoutRunsUnderDeliveryGateOnly pins the lock shape the fanout keeps:
// deliveries run with deliveryMu held and projectionMu released. The observation
// is TryLock from inside afterBroadcastConnectionLookup, the seam deliverFanout
// calls while the delivery gate is held and before each enqueue.
//
// deliveryMu is what serializes each connection's enqueues into routing order,
// and it is taken before projectionMu is released. The fanout's payload work
// therefore stays under deliveryMu: moving it ahead of that handoff would hold
// projectionMu — which every subscription capture also takes — across an encode.
// This test fails if a change pushes fanout work under projectionMu instead.
func TestFanoutRunsUnderDeliveryGateOnly(t *testing.T) {
	no := false
	s := NewServer(ServerConfig{})
	for i := range 4 {
		conn := s.NewConnection("conn-" + strconv.Itoa(i))
		conn.setInitialized()
		s.registerConnection(conn)
		conn.Subscribe("thread")
		selection := appwire.ThreadContentParams{}
		if i%2 == 0 {
			selection = appwire.ThreadContentParams{IncludeThinking: &no}
		}
		s.subs.setContent(conn.id, "thread", 0, apphistory.NewFilter(selection.Selection()))
	}
	observed := 0
	s.afterBroadcastConnectionLookup = func(*Connection) {
		observed++
		// The projection gate is released before deliveries run, so it must be
		// free here; holding it would serialize the encode against every capture.
		if s.projectionMu.TryLock() {
			s.projectionMu.Unlock()
		} else {
			t.Error("delivery ran while holding the projection gate")
		}
		// The delivery gate is held here; without it, a later record's
		// deliveries could overtake this one.
		if s.deliveryMu.TryLock() {
			s.deliveryMu.Unlock()
			t.Error("delivery ran outside the delivery gate, so enqueue order is not serialized")
		}
	}
	s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "thread",
		Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2},
		Items:    []appwire.ThreadItem{{Type: "agentMessage", ID: "a1"}, {Type: "reasoning", ID: "r1"}},
	})
	if observed != 4 {
		t.Fatalf("seam observed %d deliveries, want 4", observed)
	}
}

// TestFanoutEncodesPerDistinctSelectionNotPerSubscriber pins the cost property
// the fanout change exists for: thirty-two subscribers spread over two filtered
// selections make the gate pay for two payload encodes, not thirty-two. It
// fails with per-subscriber encoding and needs no timing measurement to do it,
// so it holds on a loaded host.
func TestFanoutEncodesPerDistinctSelectionNotPerSubscriber(t *testing.T) {
	no := false
	s := NewServer(ServerConfig{})
	for i := range 32 {
		conn := s.NewConnection("conn-" + strconv.Itoa(i))
		conn.setInitialized()
		s.registerConnection(conn)
		conn.Subscribe("thread")
		selection := appwire.ThreadContentParams{IncludeThinking: &no}
		if i%2 == 1 {
			selection = appwire.ThreadContentParams{IncludeToolCalls: &no}
		}
		s.subs.setContent(conn.id, "thread", 0, apphistory.NewFilter(selection.Selection()))
	}
	var gates int
	s.deliveryGateHeld = func(time.Duration) { gates++ }
	var encodes []int
	s.afterFanoutEncoded = func(got int) { encodes = append(encodes, got) }
	s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "thread",
		Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 3},
		Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "a1"},
			{Type: "reasoning", ID: "r1"},
			{Type: "commandExecution", ID: "c1", ToolName: "shell", ArgumentsJSON: `{"cmd":"ls"}`},
		},
	})
	if gates != 1 {
		t.Fatalf("delivery gate was held %d times for one broadcast, want 1", gates)
	}
	if len(encodes) != 1 {
		t.Fatalf("fanout reported %d records, want 1", len(encodes))
	}
	if encodes[0] != 2 {
		t.Fatalf("gate paid for %d payload encodes across 32 subscribers at 2 selections, want 2", encodes[0])
	}
}
