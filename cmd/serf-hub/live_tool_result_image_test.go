package main

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
)

// settledToolItem projects one image-returning read_file call through a real
// daemon projector and returns the item a live watcher's item/completed
// carries, plus the projector to feed the round's persist announcement to.
func settledToolItem(t *testing.T, sessionID, sha string, size int64) (*appprojector.AppEventProjector, appwire.ThreadItem) {
	t.Helper()
	p := appprojector.NewAppEventProjector(sessionID, "local:"+sessionID)
	p.Project(events.New(events.UserInputData{Text: "look at shot.png"}))
	p.Project(events.New(events.ToolCallStartData{
		ToolName: "read_file", CallID: "call_read", ArgumentsJSON: `{"file_path":"shot.png"}`,
	}))
	notes := p.Project(events.New(events.ToolCallEndData{
		ToolName: "read_file", CallID: "call_read", ArgumentsJSON: `{"file_path":"shot.png"}`,
		Output: "[image: png, 12 bytes, base64 data follows]",
		OutputImages: []events.OutputImage{{
			Source: events.OutputImageSourceToolResult, Name: "read_file",
			MediaType: "image/png", Size: size, SHA: sha,
		}},
	}))
	return p, itemFromNotifications(t, notes)
}

func itemFromNotifications(t *testing.T, notes []appprojector.AppNotification) appwire.ThreadItem {
	t.Helper()
	for _, note := range notes {
		if note.Method != appwire.NotifyItemCompleted {
			continue
		}
		params, ok := note.Params.(appwire.ItemLifecycleParams)
		if !ok {
			t.Fatalf("item/completed params are %T, want appwire.ItemLifecycleParams", note.Params)
		}
		return params.Item
	}
	t.Fatalf("no item/completed among %d notifications", len(notes))
	return appwire.ThreadItem{}
}

// TestALiveImageReadThumbnailPointsAtAServableRoute is the composition kata
// v3dv is about, end to end across the daemon's projector and this relay.
//
// A read_file of an image has two possible thumbnails: the sha-addressed route
// this hub serves by scanning the transcript, and the file-backed /doc/image
// route it serves by re-reading the file the call named. They describe the same
// bytes, so the relay's dedup keeps exactly one — and the sha-addressed one
// wins. That is right once the round has been written and wrong before it: the
// scan finds nothing, the browser gets a 404, and a thumbnail that fails to
// load leaves the strip for good, so the reader ends up with no image where the
// file-backed route would have shown one.
//
// So while the round is still running, the item carries no sha descriptor at
// all and the file-backed route is what the browser gets.
func TestALiveImageReadThumbnailPointsAtAServableRoute(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "02wMz5Txv733WHFsVy66SR"
	sha := imageSha(png)

	projector, settled := settledToolItem(t, sessionID, sha, int64(len(png)))
	item := enrichedItem(t, sessionID, cwd, settled)
	if len(item.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want exactly one thumbnail for the call", item.OutputImages)
	}
	if want := "/doc/image?session=" + sessionID + "&path=shot.png"; item.OutputImages[0].URL != want {
		t.Fatalf("OutputImages[0].URL=%q, want %q — the only route servable before the round is written",
			item.OutputImages[0].URL, want)
	}

	// Once the round's results are in the transcript the sha route works, and
	// it is the honest one: it serves the bytes the tool actually returned,
	// not whatever that path holds now.
	released := itemFromNotifications(t, projector.Project(events.New(events.ToolResultImagesPersistedData{
		CallIDs: []string{"call_read"},
	})))
	item = enrichedItem(t, sessionID, cwd, released)
	if len(item.OutputImages) != 1 {
		t.Fatalf("released OutputImages=%+v, want the two mechanisms deduped to one", item.OutputImages)
	}
	if want := "/s/" + sessionID + "/images/" + sha; item.OutputImages[0].URL != want {
		t.Fatalf("released OutputImages[0].URL=%q, want the sha route %q", item.OutputImages[0].URL, want)
	}
}
