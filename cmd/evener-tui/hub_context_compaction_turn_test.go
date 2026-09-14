package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// A compaction that happens while a turn is running now arrives as its own
// turn — turn/started, the announcement, turn/completed — because that is how
// the CONTEXT_COMPACTION record groups on reload. The session view must show
// the announcement in that turn and leave the interrupted turn's own items
// alone, and the turn the client believes is active must follow the frames
// rather than stay on the turn the compaction closed.
func TestHubModelMidTurnCompactionOpensItsOwnTurn(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "th_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1", Ref: "local:th_1", Turn: appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type: "agentMessage", ID: "agent_1", TurnID: "turn_1", Text: "before the compaction", Status: appwire.TurnStatusCompleted,
			},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1", Ref: "local:th_1", Turn: appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusInProgress},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_2",
			"item": appwire.ThreadItem{
				Type:        "systemMessage",
				ID:          "item_context_compaction_1",
				TurnID:      "turn_2",
				Description: "Context compaction",
				Text:        "Layer: checkpoint\nTurns: 42 -> 8",
				EventKind:   appwire.ThreadItemEventKindContextCompaction,
				Status:      appwire.TurnStatusCompleted,
			},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
			"threadId": "th_1",
			"ref":      "local:th_1",
			"turn":     appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusCompleted},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID: "th_1", Ref: "local:th_1", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		}).Notification,
	})

	got := updated.(hubModel)
	var system, assistant []transcript.ChatMessage
	for _, msg := range got.session.messages {
		switch msg.Kind {
		case transcript.MsgSystem:
			system = append(system, msg)
		case transcript.MsgAssistant:
			assistant = append(assistant, msg)
		}
	}
	if len(assistant) != 1 || assistant[0].TurnID != "turn_1" {
		t.Fatalf("assistant messages = %+v, want the one item of the turn the compaction interrupted", assistant)
	}
	if len(system) != 1 {
		t.Fatalf("system messages = %+v, want the compaction announcement", system)
	}
	if system[0].TurnID != "turn_2" {
		t.Fatalf("compaction announcement turn = %q, want its own turn_2", system[0].TurnID)
	}
	// The round did not end because the compaction's turn did: the session is
	// still working, and the frames say so.
	if !got.session.processing || got.detail.State != appwire.ThreadStatusActive {
		t.Fatalf("after a mid-turn compaction: processing=%v state=%q, want the session still active", got.session.processing, got.detail.State)
	}
}

// A fold runs several layers in one flush and each is now its own turn on the
// wire. The session view must show one line per layer, in its own turn, rather
// than folding the later layers into the first one's.
func TestHubModelEveryCompactionLayerRendersInItsOwnTurn(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "th_1"}

	var updated tea.Model = m
	for i, layer := range []string{"checkpoint", "summarize"} {
		turnID := fmt.Sprintf("turn_%d", i+1)
		updated, _ = updated.(hubModel).Update(hubNotificationMsg{
			ok: true,
			notification: *appwire.NotificationMessage(appwire.NotifyTurnStarted, appwire.TurnStartedParams{
				ThreadID: "th_1", Ref: "local:th_1", Turn: appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress},
			}).Notification,
		})
		updated, _ = updated.(hubModel).Update(hubNotificationMsg{
			ok: true,
			notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
				"threadId": "th_1",
				"turnId":   turnID,
				"item": appwire.ThreadItem{
					Type:        "systemMessage",
					ID:          fmt.Sprintf("item_context_compaction_%d", i+1),
					TurnID:      turnID,
					Description: "Context compaction",
					Text:        "Layer: " + layer,
					EventKind:   appwire.ThreadItemEventKindContextCompaction,
					Status:      appwire.TurnStatusCompleted,
				},
			}).Notification,
		})
		updated, _ = updated.(hubModel).Update(hubNotificationMsg{
			ok: true,
			notification: *appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
				"threadId": "th_1",
				"ref":      "local:th_1",
				"turn":     appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted},
			}).Notification,
		})
	}

	var system []transcript.ChatMessage
	for _, msg := range updated.(hubModel).session.messages {
		if msg.Kind == transcript.MsgSystem {
			system = append(system, msg)
		}
	}
	if len(system) != 2 {
		t.Fatalf("system messages = %+v, want one per compaction layer", system)
	}
	if system[0].TurnID == system[1].TurnID {
		t.Fatalf("both layers rendered in turn %q, want a turn each", system[0].TurnID)
	}
	if !strings.Contains(system[0].Text, "checkpoint") || !strings.Contains(system[1].Text, "summarize") {
		t.Fatalf("layers rendered out of order or with the wrong text: %+v", system)
	}
}
