package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.

// A clone must carry OutputImages' nilness both ways: nil (never had images)
// stays nil, and an explicit empty list (the removal) stays non-nil and empty.
// Input images carry no such rule -- see appwire.MergeInputImages -- so this
// test does not assert their nilness.
func TestCloneCarriesImageListNilnessBothWays(t *testing.T) {
	removed := cloneAppThreadItem(appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{},
		Images:       []appwire.InputItem{},
	})
	if removed.OutputImages == nil {
		t.Fatal("cloned OutputImages is nil, so the removal this item announces is lost")
	}

	never := cloneAppThreadItem(appwire.ThreadItem{ID: "item_2"})
	if never.OutputImages != nil {
		t.Fatalf("cloned OutputImages=%+v for an item that never had images, want nil", never.OutputImages)
	}
}

// The end-to-end path: a tool call whose result images cannot be served yet
// settles with them withheld, and the overlay frame the server records for its
// clients must carry the empty list. This asserts the recorded JSON, because
// nil and empty are the same Go length and differ only in what is encoded.
func TestRecordedOverlayFrameCarriesTheExplicitEmptyOutputImageList(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("look at this")))
	srv := st.srv
	srv.RecordAppEvent(threadEvent("th_1", events.RoundStartedData{RoundID: "r_1"}))
	srv.RecordAppEvent(threadEvent("th_1", events.ToolCallStartData{ToolName: "read_file", CallID: "call_shot"}))
	srv.RecordAppEvent(threadEvent("th_1", events.ToolCallEndData{
		ToolName: "read_file",
		CallID:   "call_shot",
		Output:   "read shot.png",
		// sha-addressed, no URL: no server can serve these bytes yet, so
		// the overlay withholds them and the item settles with none.
		OutputImages: []events.OutputImage{{
			Source:    events.OutputImageSourceToolResult,
			Name:      "shot.png",
			MediaType: "image/png",
			SHA:       "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		}},
	}))

	raw, found := lastOverlayItemFrameFields(t, srv, "th_1")
	if !found {
		t.Fatal("no overlay/upserted frame was recorded for the settled tool call")
	}
	images, present := raw["outputImages"]
	if !present {
		t.Fatalf("the recorded frame carries no outputImages key: %v", raw)
	}
	if string(images) != "[]" {
		t.Fatalf("outputImages=%s, want an explicit empty list", images)
	}
	if _, inputs := raw["images"]; inputs {
		t.Fatalf("the recorded frame carries images=%s for a tool item that never had input images", raw["images"])
	}

	// And the read path: the overlay a thread/read carries is a copy, one more
	// place the empty list has to survive -- it is what a client hydrates from.
	var settled *appwire.ThreadItem
	for _, item := range st.read(t).Overlay {
		if item.CallID == "call_shot" {
			settled = &item.Item
		}
	}
	if settled == nil {
		t.Fatal("the read's overlay has no item for the settled tool call")
	}
	encoded, err := json.Marshal(settled)
	if err != nil {
		t.Fatalf("Marshal the read item: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Unmarshal the read item: %v", err)
	}
	if string(fields["outputImages"]) != "[]" {
		t.Fatalf("the read item encodes outputImages=%s, want []", fields["outputImages"])
	}
}

// lastOverlayItemFrameFields decodes the thread item of the last
// overlay/upserted frame the server recorded, as raw JSON fields, so a test
// can tell an absent key from an empty one.
func lastOverlayItemFrameFields(t *testing.T, srv *Server, threadID string) (map[string]json.RawMessage, bool) {
	t.Helper()
	var out map[string]json.RawMessage
	found := false
	for _, n := range srv.AppNotificationsAfter(0, threadID) {
		if n.Notification.Method != appwire.NotifyOverlayUpserted {
			continue
		}
		var frame struct {
			Item struct {
				Item map[string]json.RawMessage `json:"item"`
			} `json:"item"`
		}
		if err := json.Unmarshal(n.Notification.Params, &frame); err != nil {
			t.Fatalf("decode overlay/upserted params: %v", err)
		}
		out, found = frame.Item.Item, true
	}
	return out, found
}
