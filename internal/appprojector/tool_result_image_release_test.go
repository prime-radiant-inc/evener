package appprojector

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// toolResultImage is the descriptor shape the agent mints for bytes that came
// back inside a tool result: addressed by sha, with no URL, because the route
// that serves them belongs to whichever server publishes the thread.
var toolResultImage = events.OutputImage{
	Source:    events.OutputImageSourceToolResult,
	Name:      "read_file",
	MediaType: "image/png",
	Size:      12,
	SHA:       "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
}

func projectorWithCompletedImageTool(t *testing.T, callID string) (*AppEventProjector, appwire.ThreadItem) {
	t.Helper()
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.New(events.UserInputData{Text: "look at this"}))
	p.Project(events.New(events.ToolCallStartData{ToolName: "read_file", CallID: callID}))
	notes := p.Project(events.New(events.ToolCallEndData{
		ToolName:     "read_file",
		CallID:       callID,
		Output:       "read shot.png",
		OutputImages: []events.OutputImage{toolResultImage},
	}))
	return p, notificationThreadItem(t, notes, appwire.NotifyItemCompleted)
}

// TestToolResultImageDescriptorWaitsForItsBytes is the wire half of kata v3dv.
// A sha-addressed tool-result descriptor is a promise that some server can
// serve those bytes, and until the round's tool-result turn is written no
// server can: the bytes are on nobody's disk and in nobody's memory. Putting
// the descriptor on the wire at call end therefore hands the client a URL that
// 404s, and a thumbnail that fails to load is dropped for good. The projector
// holds it until the round says the bytes have landed, then re-sends the same
// item with it.
func TestToolResultImageDescriptorWaitsForItsBytes(t *testing.T) {
	p, settled := projectorWithCompletedImageTool(t, "call_shot")
	if len(settled.OutputImages) != 0 {
		t.Fatalf("settled item OutputImages=%+v, want none: nothing can serve those bytes yet", settled.OutputImages)
	}

	out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_shot"}}))
	released := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if len(released.OutputImages) != 1 || released.OutputImages[0] != (appwire.OutputImage{
		Source: toolResultImage.Source, Name: toolResultImage.Name, MediaType: toolResultImage.MediaType,
		Size: toolResultImage.Size, SHA: toolResultImage.SHA,
	}) {
		t.Fatalf("released OutputImages=%+v, want the descriptor the tool call minted", released.OutputImages)
	}

	// Everything else has to match the item the client already has: this is a
	// second item/completed for an id it settled, and clients replace a
	// completed item wholesale rather than merging field by field.
	// Both sides are normalised: nil and an empty list are equal in length but not to DeepEqual.
	released.OutputImages = nil
	settled.OutputImages = nil
	if !reflect.DeepEqual(released, settled) {
		t.Fatalf("released item=%+v differs from the settled item=%+v outside its images", released, settled)
	}
}

// TestAFetchableImageDescriptorIsNotHeld keeps the hold narrow. A descriptor
// carrying its own URL names bytes a server can already serve — the
// file-backed mechanism re-reads a file the call named off disk — so holding
// it would delay a thumbnail that works for no reason at all.
func TestAFetchableImageDescriptorIsNotHeld(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.New(events.UserInputData{Text: "make a chart"}))
	p.Project(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "call_sh"}))
	notes := p.Project(events.New(events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_sh",
		Output:   "wrote out.png",
		OutputImages: []events.OutputImage{{
			Source: "shell-path", Name: "out.png", MediaType: "image/png",
			URL: "/doc/image?session=01&path=out.png", Path: "out.png",
		}},
	}))

	item := notificationThreadItem(t, notes, appwire.NotifyItemCompleted)
	if len(item.OutputImages) != 1 || item.OutputImages[0].URL == "" {
		t.Fatalf("OutputImages=%+v, want the already-fetchable descriptor to ride the settled item", item.OutputImages)
	}
}

// TestAnAnnouncementForANonImageCallSaysNothing keeps the release from
// inventing wire traffic. Only a call whose descriptor was actually held has
// anything to re-send.
func TestAnAnnouncementForANonImageCallSaysNothing(t *testing.T) {
	p, _ := projectorWithCompletedImageTool(t, "call_shot")
	if out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_other"}})); len(out) != 0 {
		t.Fatalf("notifications=%+v, want none for a call nothing is held for", out)
	}
	// The real call's descriptor is still held, and still arrives.
	if out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_shot"}})); len(out) != 1 {
		t.Fatalf("notifications=%+v, want the held descriptor to survive an unrelated announcement", out)
	}
}

// TestAHeldDescriptorIsReleasedOnlyOnce keeps a replayed or duplicated
// announcement from re-sending an item the client already has.
func TestAHeldDescriptorIsReleasedOnlyOnce(t *testing.T) {
	p, _ := projectorWithCompletedImageTool(t, "call_shot")
	if out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_shot"}})); len(out) != 1 {
		t.Fatalf("first release notifications=%+v, want one", out)
	}
	if out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_shot"}})); len(out) != 0 {
		t.Fatalf("second release notifications=%+v, want none", out)
	}
}

// TestANewTurnDropsUnreleasedDescriptors bounds what the projector keeps. A
// round that is interrupted before its results are written never announces
// them, so its held items would otherwise accumulate for the life of the
// session.
func TestANewTurnDropsUnreleasedDescriptors(t *testing.T) {
	p, _ := projectorWithCompletedImageTool(t, "call_shot")
	p.Project(events.New(events.UserInputData{Text: "never mind"}))

	if out := p.Project(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"call_shot"}})); len(out) != 0 {
		t.Fatalf("notifications=%+v, want none: the turn that held this descriptor is over", out)
	}
}

// An item that carried images and now carries none must say so on the wire.
// The held-descriptor path above is the hub's one image removal: the item the
// tool call settled had images, and the frame the client receives has none. A
// client replaces its copy of an item id wholesale, but only for the fields the
// frame carries, so an absent key reads as "unchanged" and a copy that already
// holds the images (a page read, or an earlier frame) keeps showing them.
// Encoding is where this is decided, which is why this test marshals the frame
// rather than reading the Go slice: encoding/json omits a zero-length slice
// under omitempty whether it is nil or not (#1580 made the TypeScript reducer
// treat an explicit [] as a removal, which nothing could reach until here).
func TestHeldToolResultImagesSendAnExplicitEmptyList(t *testing.T) {
	_, settled := projectorWithCompletedImageTool(t, "call_shot")

	encoded, err := json.Marshal(settled)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, present := fields["outputImages"]
	if !present {
		t.Fatalf("the frame for an item whose images were withheld carries no outputImages key: %s", encoded)
	}
	if string(raw) != "[]" {
		t.Fatalf("outputImages=%s, want an explicit empty list", raw)
	}
}

// The other half of the rule: an item that never had images carries no key at
// all. An empty list means "they are gone"; sending it for every image-less
// item would put two more keys on every frame and tell clients nothing.
func TestAnItemThatNeverHadImagesCarriesNoImageKeys(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.New(events.UserInputData{Text: "no pictures here"}))
	p.Project(events.New(events.ToolCallStartData{ToolName: "read_file", CallID: "call_plain"}))
	notes := p.Project(events.New(events.ToolCallEndData{
		ToolName: "read_file",
		CallID:   "call_plain",
		Output:   "read notes.txt",
	}))
	settled := notificationThreadItem(t, notes, appwire.NotifyItemCompleted)

	encoded, err := json.Marshal(settled)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"images", "outputImages"} {
		if raw, present := fields[key]; present {
			t.Fatalf("an item that never had images carries %s=%s; want the key absent", key, raw)
		}
	}
}
