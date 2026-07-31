package server

import (
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// The prelude turn (appwire.SystemPreludeTurnID) holds content from BEFORE
// the session's first real turn by definition. Nothing orders a session's
// first turn-starting request ahead of its SESSION_START-time announcements
// (server.PrepareAppIdentity says so), so the snapshot can meet turn_1's
// turn/started before the prelude's first turn/completed. Creating the
// prelude turn by appending then pins the session's startup burst - the "N
// system events" group a reader expects at the top - at the END of the
// transcript, where it stays while the first real turn streams above it.
// The prelude is the one turn whose id fixes its position: it must be
// inserted at the front whenever it is created, however late it arrives.
func TestAppTurnSnapshotInsertsLateArrivingPreludeTurnAtTheFront(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnStarted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_1","status":"inProgress"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnCompleted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_system","status":"completed","itemsView":"full","items":[{"id":"item_plugin_loaded_1","type":"systemMessage","turnId":"turn_system","text":"Plugin loaded: superpowers","status":"completed","eventKind":"plugin_loaded"}]}}`),
		}},
	}

	turns := appTurnsFromNotifications(records)
	if len(turns) != 2 || turns[0].ID != appwire.SystemPreludeTurnID || turns[1].ID != "turn_1" {
		got := make([]string, 0, len(turns))
		for _, turn := range turns {
			got = append(got, turn.ID)
		}
		t.Fatalf("turn order = %v, want [%s turn_1]", got, appwire.SystemPreludeTurnID)
	}
	if len(turns[0].Items) != 1 || turns[0].Items[0].ID != "item_plugin_loaded_1" {
		t.Fatalf("prelude items = %+v, want the one announcement item", turns[0].Items)
	}
}

// Inserting the prelude at the front shifts every existing turn's position,
// so the id -> position index must shift with it: an item upserted into
// turn_1 AFTER the prelude insert must still find turn_1 rather than
// whatever now sits at its old index.
func TestAppTurnSnapshotPreludeInsertKeepsTurnIndexHonest(t *testing.T) {
	snapshot := &appTurnSnapshot{}
	snapshot.Apply([]appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnStarted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_1","status":"inProgress"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnStarted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_2","status":"inProgress"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnCompleted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_system","status":"completed","itemsView":"full","items":[{"id":"item_plugin_loaded_1","type":"systemMessage","turnId":"turn_system","text":"Plugin loaded: superpowers","status":"completed","eventKind":"plugin_loaded"}]}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemCompleted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"item_user_1","type":"userMessage","turnId":"turn_1","text":"hi","status":"completed"}}`),
		}},
	})

	turns := snapshot.Snapshot()
	wantOrder := []string{appwire.SystemPreludeTurnID, "turn_1", "turn_2"}
	if len(turns) != len(wantOrder) {
		t.Fatalf("turns = %+v, want %d", turns, len(wantOrder))
	}
	for i, want := range wantOrder {
		if turns[i].ID != want {
			t.Fatalf("turns[%d].ID = %q, want %q (full order: %+v)", i, turns[i].ID, want, turns)
		}
	}
	turn1 := turns[1]
	if len(turn1.Items) != 1 || turn1.Items[0].ID != "item_user_1" {
		t.Fatalf("turn_1 items = %+v, want the user item upserted after the prelude insert", turn1.Items)
	}
}
