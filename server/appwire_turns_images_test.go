package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// A live image change is in the value every thread/read clones. This test
// exercises the snapshot read path: handleAppThreadRead -> appLatestItemTurns ->
// LatestItemCandidates -> NormalizeProjectedItemCompleteness -> RegroupTurnFragments,
// reading the same materialized snapshot (Server.appTurns: "Every turn read -- thread/read,
// the latest window, an older page -- clones or windows this and nothing else").
// Image retention happens through the snapshot: LatestItemCandidates returns item-bearing
// candidates from the snapshot, and RegroupTurnFragments clones them, preserving their
// images. An item-bearing frame a client folded before the response is already reflected
// in the response. Measured here through a real Server and the actual read path, for images
// the way the delta cases measure text: an item/started that carries input images, then an
// item/completed that says nothing about them, and the read response still has them, decoded.
func TestAppThreadReadKeepsLiveInputImages(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "01T")

	snapshot := &appTurnSnapshot{threadID: "01T"}
	snapshot.Apply([]appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnStarted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_1","status":"inProgress"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemStarted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"item_user_1","type":"userMessage","turnId":"turn_1","text":"look","status":"inProgress","images":[{"type":"image","mediaType":"image/png","data":"AQID","name":"shot.png"}]}}`),
		}},
	})
	installTurnSnapshotObjectForTest(srv, snapshot)

	assertLiveInputImage := func() {
		read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:01T", IncludeTurns: true})
		if err != nil {
			t.Fatalf("handleAppThreadRead: %v", err)
		}
		if len(read.Thread.Turns) != 1 || len(read.Thread.Turns[0].Items) != 1 {
			t.Fatalf("turns = %+v, want one turn with one item", read.Thread.Turns)
		}
		images := read.Thread.Turns[0].Items[0].Images
		if len(images) != 1 {
			t.Fatalf("Images = %+v, want the live image the frame carried", images)
		}
		if images[0].Name != "shot.png" || images[0].MediaType != "image/png" || string(images[0].Data) != "\x01\x02\x03" {
			t.Fatalf("Images[0] = %+v, want name shot.png, mediaType image/png, decoded data 01 02 03", images[0])
		}
	}
	assertLiveInputImage()

	// The settle says nothing about images; the read response still has them.
	snapshot.Apply([]appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemCompleted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"item_user_1","type":"userMessage","turnId":"turn_1","text":"look","status":"completed"}}`),
		}},
	})
	assertLiveInputImage()
}

// installTurnSnapshotObjectForTest installs an already-built appTurnSnapshot
// (one a test has folded live notifications into) the way appwire_turns_paging_test.go's
// installTurnSnapshotForTest installs a static turn list: directly, under the
// server's own lock, so appTurnSnapshotForID finds it installed for reads.
func installTurnSnapshotObjectForTest(srv *Server, snapshot *appTurnSnapshot) {
	srv.mu.Lock()
	srv.appTurns = snapshot
	srv.mu.Unlock()
}
