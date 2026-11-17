package appserver

import (
	"strconv"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

// TestFanoutEncodesOncePerDistinctSelection pins the fanout's work per record:
// payload encodes are charged per distinct content selection, never per
// subscriber, so adding subscribers at a filter setting a hub already serves
// costs no further encoding.
//
// The count is observed through afterFanoutEncoded, the seam deliverFanout
// calls once per delivered record while the delivery gate is still held. It
// reports what the gate paid for, so a regression that encodes per subscriber
// fails here with no timing measurement involved.
func TestFanoutEncodesOncePerDistinctSelection(t *testing.T) {
	no := false
	for _, test := range []struct {
		name        string
		subscribers []appwire.ThreadContentParams
		wantEncodes int
	}{
		{
			name: "one filtered selection shared by four subscribers",
			subscribers: []appwire.ThreadContentParams{
				{IncludeThinking: &no}, {IncludeThinking: &no}, {IncludeThinking: &no}, {IncludeThinking: &no},
			},
			wantEncodes: 1,
		},
		{
			name: "two filtered selections across six subscribers",
			subscribers: []appwire.ThreadContentParams{
				{IncludeThinking: &no}, {IncludeThinking: &no}, {IncludeThinking: &no},
				{IncludeToolCalls: &no}, {IncludeToolCalls: &no}, {IncludeToolCalls: &no},
			},
			wantEncodes: 2,
		},
		{
			name: "thirty-two subscribers at two filtered selections",
			subscribers: func() []appwire.ThreadContentParams {
				all := make([]appwire.ThreadContentParams, 0, 32)
				for i := range 32 {
					if i%2 == 0 {
						all = append(all, appwire.ThreadContentParams{IncludeThinking: &no})
						continue
					}
					all = append(all, appwire.ThreadContentParams{IncludeToolCalls: &no})
				}
				return all
			}(),
			wantEncodes: 2,
		},
		{
			name:        "a full view has no filter and reuses the recorded frame",
			subscribers: []appwire.ThreadContentParams{{}, {}, {}},
			wantEncodes: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			for i, selection := range test.subscribers {
				conn := s.NewConnection("conn-" + strconv.Itoa(i))
				conn.setInitialized()
				s.registerConnection(conn)
				conn.Subscribe("thread")
				s.subs.setContent(conn.id, "thread", 0, apphistory.NewFilter(selection.Selection()))
			}
			var encodes []int
			s.afterFanoutEncoded = func(got int) { encodes = append(encodes, got) }
			s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
				ThreadID: "thread",
				Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2},
				Items: []appwire.ThreadItem{
					{Type: "agentMessage", ID: "a1"},
					{Type: "reasoning", ID: "r1"},
					{Type: "commandExecution", ID: "c1", ToolName: "shell"},
				},
			})
			if len(encodes) != 1 {
				t.Fatalf("fanout reported %d records for one broadcast, want 1: %v", len(encodes), encodes)
			}
			if encodes[0] != test.wantEncodes {
				t.Fatalf("fanout encoded %d frames, want %d (one per distinct selection)", encodes[0], test.wantEncodes)
			}
		})
	}
}

// TestFanoutOverlayDeltaNeverReencodes pins the other half of the cost bound:
// the notification every streaming turn sends many times — an overlay delta —
// reaches its readers as the recorded bytes and is never re-encoded for any
// selection.
func TestFanoutOverlayDeltaNeverReencodes(t *testing.T) {
	no := false
	s := NewServer(ServerConfig{})
	for i, selection := range []appwire.ThreadContentParams{{}, {IncludeThinking: &no}} {
		conn := s.NewConnection("conn-" + strconv.Itoa(i))
		conn.setInitialized()
		s.registerConnection(conn)
		conn.Subscribe("thread")
		s.subs.setContent(conn.id, "thread", 0, apphistory.NewFilter(selection.Selection()))
	}
	var encodes []int
	s.afterFanoutEncoded = func(got int) { encodes = append(encodes, got) }
	s.Broadcast("thread", appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
		ThreadID: "thread",
		Item:     appwire.OverlayItem{Key: "stream:s1:agentMessage", Kind: appwire.OverlayStream, StreamID: "s1", Item: appwire.ThreadItem{Type: "agentMessage", ID: "a1"}},
	})
	if len(encodes) != 1 || encodes[0] != 1 {
		t.Fatalf("upsert encodes = %v, want one frame for the one filtered selection", encodes)
	}
	encodes = nil
	s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: "stream:s1:agentMessage", Field: appwire.OverlayDeltaText, Delta: "chunk"})
	if len(encodes) != 1 || encodes[0] != 0 {
		t.Fatalf("delta encodes = %v, want no encode for any selection", encodes)
	}
}
