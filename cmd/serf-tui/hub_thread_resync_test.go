package main

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/internal/appserver"
)

// deadGenerationThread is the authoritative snapshot a daemon relaunched behind
// the hub answers thread/read with: the persisted transcript, which ends at
// turn_4. The dead generation's turn_5 is absent because a between-turns
// announcement mints a turn id that never becomes a transcript entry, which is
// exactly why the replacement daemon's projector re-seeds turn_5 and hands it
// to the next live turn (kata 8nyk).
func deadGenerationThread() appwire.Thread {
	return appwire.Thread{
		ID:            "01SEND",
		SessionID:     "01SEND",
		Name:          "send task",
		ModelProvider: "gpt-5",
		Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Source:        "local",
		Serf:          appwire.SerfThread{Ref: "local:01SEND"},
		Turns: []appwire.Turn{{
			ID:     "turn_4",
			Status: appwire.TurnStatusCompleted,
			Items: []appwire.ThreadItem{
				{Type: "userMessage", ID: "item_user_1", TurnID: "turn_4", Text: "check the deploy", Status: "completed"},
				{Type: "agentMessage", ID: "item_agent_2", TurnID: "turn_4", Text: "deploy looks healthy", Status: "completed"},
			},
		}},
	}
}

// preRelaunchMessages is the model a TUI session is still holding when its
// daemon is replaced. The hub relay handle survives the outage and nothing on
// the TUI's own path tears session state down, so these rows — including the
// dead generation's turn_5 announcement — are what a replacement daemon's
// frames would otherwise be folded into.
func preRelaunchMessages() []transcript.ChatMessage {
	return []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "check the deploy", TurnID: "turn_4", TurnIndex: 4, ItemID: "item_user_1"},
		{Kind: transcript.MsgAssistant, Text: "deploy looks healthy", TurnID: "turn_4", TurnIndex: 4, ItemID: "item_agent_2"},
		{Kind: transcript.MsgSystem, Text: "hook post-turn finished", TurnID: "turn_5", TurnIndex: 5, ItemID: "item_hook_3"},
	}
}

// serf/thread/resync is the hub's only "the model you hold belongs to a daemon
// that is gone" signal. The TUI must answer it the way the web client does — a
// targeted re-read of that ref — rather than folding the replacement daemon's
// frames into state whose turn ids the replacement is about to reuse.
func TestHubModelThreadResyncRereadsInsteadOfKeepingDeadGenerationState(t *testing.T) {
	var reads []appwire.ThreadReadParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			reads = append(reads, params)
			return appwire.ThreadReadResponse{Thread: deadGenerationThread()}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.messages = preRelaunchMessages()

	cmd := m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
	}).Notification)
	if cmd == nil {
		t.Fatal("serf/thread/resync issued no command; the TUI kept the dead daemon's model")
	}
	updated, _ := m.Update(cmd())
	m = updated.(hubModel)

	if len(reads) != 1 {
		t.Fatalf("thread/read calls=%d (%+v), want exactly one re-read", len(reads), reads)
	}
	if reads[0].Ref != "local:01SEND" || !reads[0].IncludeTurns || !reads[0].Subscribe {
		t.Fatalf("re-read params=%+v, want a subscribed full-transcript read of the viewed ref", reads[0])
	}
	// Additive, like the web store's resync re-read: the TUI layers child
	// transcript subscriptions onto this connection for the subagent rail, and
	// replaceSubscription would drop every one of them.
	if reads[0].ReplaceSubscription {
		t.Fatalf("re-read params=%+v, want an additive re-subscribe so watched-child subscriptions survive", reads[0])
	}

	for _, msg := range m.session.messages {
		if strings.Contains(msg.Text, "hook post-turn finished") {
			t.Fatalf("messages=%+v, want the dead generation's turn_5 row gone after the re-read", m.session.messages)
		}
	}
	if len(m.session.messages) != 2 || m.session.messages[1].Text != "deploy looks healthy" || m.session.messages[1].TurnID != "turn_4" {
		t.Fatalf("messages=%+v, want the authoritative transcript from the re-read", m.session.messages)
	}
	if m.detail.ActiveTurnID != "" {
		t.Fatalf("active turn id=%q, want the dead generation's active turn dropped", m.detail.ActiveTurnID)
	}

	// The replacement daemon's first live turn reuses turn_5. With the dead
	// generation's turn_5 row gone, its frames own that turn outright.
	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
		ThreadID: "01SEND", Ref: "local:01SEND", Turn: appwire.Turn{ID: "turn_5"},
	}).Notification)
	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyItemStarted, appwire.ItemLifecycleParams{
		ThreadID: "01SEND", Ref: "local:01SEND", TurnID: "turn_5",
		Item: appwire.ThreadItem{Type: "userMessage", ID: "item_user_9", TurnID: "turn_5", Text: "redeploy now"},
	}).Notification)

	var turn5 []transcript.ChatMessage
	for _, msg := range m.session.messages {
		if msg.TurnID == "turn_5" {
			turn5 = append(turn5, msg)
		}
	}
	if len(turn5) != 1 || turn5[0].Text != "redeploy now" {
		t.Fatalf("turn_5 rows=%+v, want only the replacement generation's own frames", turn5)
	}
}

// A resync names one ref. The TUI holds transcript state for the viewed session
// only, so a resync for anything else has nothing to repair and must not put a
// read on the wire.
func TestHubModelThreadResyncForAnotherSessionIssuesNoRead(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.messages = preRelaunchMessages()

	cmd := m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{
		ThreadID: "01OTHER",
		Ref:      "local:01OTHER",
	}).Notification)
	if cmd != nil {
		t.Fatal("resync for another session issued a command; the viewed session must not be re-read")
	}
	if len(m.session.messages) != 3 {
		t.Fatalf("messages=%+v, want the viewed session's transcript untouched", m.session.messages)
	}
}
