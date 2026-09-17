package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// A live image change is in the value every thread/read clones. The read's cut
// is taken inside the projection gate (appThreadReadSnapshot), and the turns it
// returns are this materialized snapshot (Server.appTurns: "Every turn read --
// thread/read, the latest window, an older page -- clones or windows this and
// nothing else"), so an item-bearing frame a client folded before the response
// is already reflected in the response. Measured here for images the way the
// delta cases measure text: an item/started that carries input images, then an
// item/completed that says nothing about them, and the snapshot still has them.
func TestAppTurnSnapshotKeepsLiveInputImages(t *testing.T) {
	snapshot := &appTurnSnapshot{}
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

	turns := snapshot.Snapshot()
	if len(turns) != 1 || len(turns[0].Items) != 1 {
		t.Fatalf("turns = %+v, want one turn with one item", turns)
	}
	if len(turns[0].Items[0].Images) != 1 || turns[0].Items[0].Images[0].Name != "shot.png" {
		t.Fatalf("Images = %+v, want the live image the frame carried", turns[0].Items[0].Images)
	}

	// The settle says nothing about images; the snapshot a read clones keeps them.
	snapshot.Apply([]appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemCompleted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"item_user_1","type":"userMessage","turnId":"turn_1","text":"look","status":"completed"}}`),
		}},
	})
	turns = snapshot.Snapshot()
	if len(turns[0].Items[0].Images) != 1 {
		t.Fatalf("Images after settle = %+v, want the image kept (mergeAppThreadItem's len==0 rule)", turns[0].Items[0].Images)
	}
}
