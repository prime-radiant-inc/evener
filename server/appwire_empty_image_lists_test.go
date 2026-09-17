package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// An explicit empty image list is the hub's only way to say "the pictures are
// gone" (appwire.ThreadItem.OutputImages). Every layer between the projector
// and the socket therefore has to carry the difference between an empty list
// and no list at all, and the two below are where it was lost.

// The merge treated an empty incoming list as "the frame said nothing" and
// copied the images back, so a removal never reached a client whose item the
// snapshot had already seen with images.
func TestMergeKeepsAnExplicitEmptyOutputImageList(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
		Images:       []appwire.InputItem{{Type: "image", Name: "in.png"}},
	}
	incoming := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{},
		Images:       []appwire.InputItem{},
	}

	merged := mergeAppThreadItem(existing, incoming)
	if merged.OutputImages == nil || len(merged.OutputImages) != 0 {
		t.Fatalf("merged OutputImages=%+v, want the frame's explicit empty list", merged.OutputImages)
	}
	if merged.Images == nil || len(merged.Images) != 0 {
		t.Fatalf("merged Images=%+v, want the frame's explicit empty list", merged.Images)
	}
}

// Absent stays absent: a frame that carries no list at all must not erase what
// the snapshot holds, which is what the length check was really for.
func TestMergeKeepsExistingImagesWhenTheFrameCarriesNone(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
		Images:       []appwire.InputItem{{Type: "image", Name: "in.png"}},
	}

	merged := mergeAppThreadItem(existing, appwire.ThreadItem{ID: "item_1"})
	if len(merged.OutputImages) != 1 || len(merged.Images) != 1 {
		t.Fatalf("merged images=%+v/%+v, want the snapshot's own kept", merged.Images, merged.OutputImages)
	}
}

// The clone flattened an empty non-nil list to nil (append to a nil slice
// yields nil), and minted an empty non-nil one out of nil input, so it both
// erased removals and announced them where none happened.
func TestCloneCarriesImageListNilnessBothWays(t *testing.T) {
	removed := cloneAppThreadItem(appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{},
		Images:       []appwire.InputItem{},
	})
	if removed.OutputImages == nil {
		t.Fatal("cloned OutputImages is nil, so the removal this item announces is lost")
	}
	if removed.Images == nil {
		t.Fatal("cloned Images is nil, so the removal this item announces is lost")
	}

	never := cloneAppThreadItem(appwire.ThreadItem{ID: "item_2"})
	if never.OutputImages != nil {
		t.Fatalf("cloned OutputImages=%+v for an item that never had images, want nil", never.OutputImages)
	}
	if never.Images != nil {
		t.Fatalf("cloned Images=%+v for an item that never had images, want nil", never.Images)
	}
}

// The end-to-end path: a tool call whose result images cannot be served yet
// settles with them withheld, and the frame the server records for its clients
// must carry the empty list through the reduction layer
// (applyLifecycleAndReturn) that merges it into the live snapshot. This asserts
// the recorded JSON, because nil and empty are the same Go length and differ
// only in what is encoded.
func TestRecordedItemFrameCarriesTheExplicitEmptyOutputImageList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "look at this"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallStart,
		SessionID: "th_1",
		Data:      events.ToolCallStartData{ToolName: "read_file", CallID: "call_shot"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: "th_1",
		Data: events.ToolCallEndData{
			ToolName: "read_file",
			CallID:   "call_shot",
			Output:   "read shot.png",
			// sha-addressed, no URL: no server can serve these bytes yet, so
			// the projector withholds them and the item settles with none.
			OutputImages: []events.OutputImage{{
				Source:    events.OutputImageSourceToolResult,
				Name:      "shot.png",
				MediaType: "image/png",
				SHA:       "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			}},
		},
	})

	raw, found := lastItemFrameFields(t, srv, "th_1")
	if !found {
		t.Fatal("no item/completed frame was recorded for the settled tool call")
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
}

// lastItemFrameFields decodes the item of the last item/completed frame the
// server recorded, as raw JSON fields, so a test can tell an absent key from an
// empty one.
func lastItemFrameFields(t *testing.T, srv *Server, threadID string) (map[string]json.RawMessage, bool) {
	t.Helper()
	var out map[string]json.RawMessage
	found := false
	for _, n := range srv.AppNotificationsAfter(0, threadID) {
		if n.Notification.Method != appwire.NotifyItemCompleted {
			continue
		}
		var frame struct {
			Item map[string]json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(n.Notification.Params, &frame); err != nil {
			t.Fatalf("decode item/completed params: %v", err)
		}
		out, found = frame.Item, true
	}
	return out, found
}
