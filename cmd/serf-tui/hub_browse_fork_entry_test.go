package main

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// TestHubForkDraftDivergesAtTheTranscriptEntryIndex (kata e6q0, the TUI twin of
// 0jhh) pins the number the fork draft sends as its divergence position.
//
// thread/fork's sourceTurnId is a 1-based index into the parent transcript's
// ENTRY list — the hub's parseSourceTurnID hands it straight to
// agent.ForkSessionAtUserTurn — so the only field that names it is the item's
// own TranscriptEntryIndex. A turn id coincides with that index only on a
// transcript replayed from disk; internal/appprojector numbers a live turn off
// a per-turn counter while the entry index counts every entry, so the second
// user input of this session opens turn_2 but is transcript entry 3. Forking
// from the id would cut the child at entry 2 — the assistant reply, an entry
// the user never pointed at.
func TestHubForkDraftDivergesAtTheTranscriptEntryIndex(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.width, m.height = 100, 20
	m.session.width, m.session.height = 100, 20

	// A live projector-minted turn sequence: entry 1 user, entry 2 assistant,
	// entry 3 user — but only two turns, so turn ids and entry indexes diverge.
	for _, item := range []appwire.ThreadItem{
		{Type: "userMessage", ID: "user_1", TurnID: "turn_1", TranscriptEntryIndex: 1, Text: "first task", Status: "completed"},
		{Type: "agentMessage", ID: "agent_1", TurnID: "turn_1", Text: "first reply", Status: "completed"},
		{Type: "userMessage", ID: "user_2", TurnID: "turn_2", TranscriptEntryIndex: 3, Text: "second task", Status: "completed"},
	} {
		m = applyForkEntryItem(t, m, item)
	}
	m.session.refreshViewport()
	m.sessionView()
	m.enterSessionBrowse(false)
	m.sessionView()

	m.browseSelected = 2
	if msg := m.session.messages[2]; msg.Kind != transcript.MsgUser || msg.Text != "second task" {
		t.Fatalf("selected message = %+v, want the second user message", msg)
	}
	m.startForkDraft()

	if m.forkDraft == nil {
		t.Fatalf("no fork draft started; session log: %s", forkEntrySystemLog(m))
	}
	if m.forkDraft.Turn != 3 {
		t.Fatalf("fork divergence position=%d, want 3 (the entry the user pointed at); 2 is the assistant reply that live turn_2 numbers", m.forkDraft.Turn)
	}
}

func applyForkEntryItem(t *testing.T, m hubModel, item appwire.ThreadItem) hubModel {
	t.Helper()
	raw, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: item.TurnID, Item: item})
	if err != nil {
		t.Fatalf("marshal item/completed params: %v", err)
	}
	updated, _ := m.Update(hubNotificationMsg{ok: true, notification: appwire.Notification{
		Method: appwire.NotifyItemCompleted,
		Params: raw,
	}})
	return updated.(hubModel)
}

func forkEntrySystemLog(m hubModel) string {
	var lines []string
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgSystem {
			lines = append(lines, msg.Text)
		}
	}
	return strings.Join(lines, " | ")
}
