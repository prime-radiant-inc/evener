package appprojector

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
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
	released.OutputImages = nil
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
