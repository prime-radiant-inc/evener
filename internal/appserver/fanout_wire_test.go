package appserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

// TestFilteredFanoutWireBytesMatchPreChangeGolden pins invariant 1 of the
// fanout change: every subscriber receives the same bytes it received before,
// when each delivery ran the payload through its own filter.
//
// The oracle is testdata/fanout/wire.json, generated from the tree before the
// change by broadcasting each record to one subscriber per content selection
// and recording the params each was delivered (see the fixture's own keys: five
// records across eight selections, 40 entries, with an empty string for a
// subscriber that was sent nothing). Re-deriving the expected value from this
// tree's own filter would prove nothing, so the fixture is the independent
// record of what clients saw.
func TestFilteredFanoutWireBytesMatchPreChangeGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fanout", "wire.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]string
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != 40 {
		t.Fatalf("golden holds %d entries, want 40 (5 records x 8 selections)", len(golden))
	}

	no := false
	selections := map[string]appwire.ThreadContentParams{
		"full":                {},
		"hide-thinking":       {IncludeThinking: &no},
		"hide-tools":          {IncludeToolCalls: &no},
		"hide-intents":        {IncludeToolCallIntents: &no},
		"hide-conversation":   {IncludeConversation: &no},
		"hide-system":         {IncludeSystemMessages: &no},
		"hide-diagnostics":    {IncludeDiagnostics: &no},
		"hide-thinking-tools": {IncludeThinking: &no, IncludeToolCalls: &no},
	}
	streamUpsert := appwire.OverlayUpsertedParams{ThreadID: "thread", Ref: "local:thread", Item: appwire.OverlayItem{
		Key: "stream:s1:agentMessage", Kind: appwire.OverlayStream, StreamID: "s1", RoundID: "round1",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "a1", Text: "chunk"}}}
	toolUpsert := appwire.OverlayUpsertedParams{ThreadID: "thread", Ref: "local:thread", Item: appwire.OverlayItem{
		Key: "tool:c1", Kind: appwire.OverlayTool, RoundID: "round1",
		Item: appwire.ThreadItem{Type: "commandExecution", ID: "c1", ToolName: "shell", Description: "list", ArgumentsJSON: `{"cmd":"ls"}`, Output: "out"}}}
	records := []struct {
		name   string
		method string
		params any
	}{
		{"history", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
			ThreadID: "thread", Ref: "local:thread", BootGeneration: "1", Epoch: 7,
			Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 7},
			Items: []appwire.ThreadItem{
				{Type: "userMessage", ID: "u1", Text: "hi"},
				{Type: "agentMessage", ID: "a1", Text: "hello"},
				{Type: "reasoning", ID: "r1", Text: "think"},
				{Type: "steering", ID: "s1", Source: "user", Text: "steer"},
				{Type: "steering", ID: "s2", Source: "system", Text: "notice"},
				{Type: "commandExecution", ID: "c1", ToolName: "shell", CallID: "call1", Description: "list", ArgumentsJSON: `{"cmd":"ls"}`, Output: "out"},
				{Type: "diagnostic", ID: "d1", Text: "diag"},
			},
		}},
		{"stream-upsert", appwire.NotifyOverlayUpserted, streamUpsert},
		{"stream-delta", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: streamUpsert.Item.Key, Field: appwire.OverlayDeltaText, Delta: "more"}},
		{"tool-upsert", appwire.NotifyOverlayUpserted, toolUpsert},
		{"tool-delta", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: toolUpsert.Item.Key, Field: appwire.OverlayDeltaOutput, Delta: "tail"}},
	}
	for _, record := range records {
		t.Run(record.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			conns := map[string]*Connection{}
			index := 0
			for name, selection := range selections {
				conn := s.NewConnection(name)
				conn.setInitialized()
				s.registerConnection(conn)
				conn.Subscribe("thread")
				filter := apphistory.NewFilter(selection.Selection())
				if filter != nil {
					// The fixture's subscribers were seeded with both overlay
					// identities, so a delta reaches every reader that kept one.
					filter.Seed([]appwire.OverlayItem{streamUpsert.Item, toolUpsert.Item})
				}
				s.subs.setContent(conn.id, "thread", 0, filter)
				conns[name] = conn
				index++
			}
			s.Broadcast("thread", record.method, record.params)
			for name := range selections {
				want := golden[record.name+"/"+name]
				var got string
				select {
				case frame := <-conns[name].send:
					got = string(frame.Notification.Params)
				default:
				}
				if got != want {
					t.Fatalf("%s/%s bytes = %s, want %s", record.name, name, strconv.Quote(got), strconv.Quote(want))
				}
			}
		})
	}
}
