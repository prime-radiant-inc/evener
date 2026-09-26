package tui

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// These exercise applyHubNotification's real dispatch (JSON decode + reducer
// wiring) for the read model's new notifications, not just the transcript
// package's own reducer unit tests.

func TestApplyHubNotificationHistoryUpdatedRendersItemAndTurnFailure(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		Ref: "local:root",
		Items: []appwire.ThreadItem{{
			Type: "agentMessage", ID: "item_1", TurnID: "turn_1", Text: "final answer", Version: 3,
		}},
		Turns: []appwire.Turn{{
			ID: "turn_1", Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{Message: "boom"},
		}},
	})

	if len(m.session.messages) != 2 {
		t.Fatalf("messages = %+v, want [agentMessage, system failure]", m.session.messages)
	}
	if m.session.messages[0].Kind != transcript.MsgAssistant || m.session.messages[0].Text != "final answer" {
		t.Fatalf("agentMessage row = %+v", m.session.messages[0])
	}
	if m.session.messages[1].Kind != transcript.MsgSystem {
		t.Fatalf("failure row = %+v", m.session.messages[1])
	}
}

func TestApplyHubNotificationOverlayLifecycleStreamsThenClearsOnEnd(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
		Ref: "local:root",
		Item: appwire.OverlayItem{
			Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream,
			TurnID: "turn_1", RoundID: "round_1", StreamID: "round_1/0",
			Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "strea"},
		},
	})
	sendTUINotification(t, &m, appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{
		Ref: "local:root", Key: "stream:round_1/0:agentMessage", Field: appwire.OverlayDeltaText, Delta: "ming",
	})

	if len(m.session.messages) != 1 || m.session.messages[0].Text != "streaming" {
		t.Fatalf("after upsert+delta, messages = %+v", m.session.messages)
	}

	// Interrupted with nothing recorded: the round just ends.
	sendTUINotification(t, &m, appwire.NotifyOverlayEnd, appwire.OverlayEndParams{Ref: "local:root", RoundID: "round_1"})

	if len(m.session.messages) != 0 {
		t.Fatalf("overlay/end should drop the uncovered stream row: %+v", m.session.messages)
	}
}

func TestApplyHubNotificationOverlayResetDiscardsRetriedAttempt(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{
		Ref: "local:root",
		Item: appwire.OverlayItem{
			Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, RoundID: "round_1", StreamID: "round_1/0",
			Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "will be retried"},
		},
	})
	sendTUINotification(t, &m, appwire.NotifyOverlayReset, appwire.OverlayResetParams{Ref: "local:root", StreamID: "round_1/0"})

	if len(m.session.messages) != 0 {
		t.Fatalf("overlay/reset should discard the attempt: %+v", m.session.messages)
	}
}
