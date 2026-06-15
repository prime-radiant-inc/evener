package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// TestHubModelBrowseKJMovesSelectionAndReachesFork is the regression test for the
// browse-mode fork bug: k/j must move the browse cursor across turns (auto-
// scrolling to keep it visible) so a user turn can be selected and forked. Before
// the fix, k/j only scrolled the viewport and browseSelected was pinned to the
// trailing message, so f never reached a user turn.
func TestHubModelBrowseKJMovesSelectionAndReachesFork(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.width = 100
	m.height = 12
	m.session.width = 100
	m.session.height = 12
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "first question", TurnIndex: 1},
		{Kind: transcript.MsgAssistant, Text: "first answer"},
		{Kind: transcript.MsgUser, Text: "second question", TurnIndex: 2},
		{Kind: transcript.MsgAssistant, Text: "second answer"},
	}
	m.session.refreshViewport()
	m.sessionView()
	m.enterSessionBrowse(false)
	m.sessionView()

	if m.browseSelected != 3 {
		t.Fatalf("initial selection=%d, want 3 (last message)", m.browseSelected)
	}

	// f on a non-user (trailing assistant) message must not start a fork.
	updated, _ := m.Update(key("f"))
	m = updated.(hubModel)
	if m.forkDraft != nil {
		t.Fatal("fork must not start on a non-user message")
	}

	// k walks the cursor up to the first user turn.
	for i := 0; i < 3; i++ {
		updated, _ = m.Update(key("k"))
		m = updated.(hubModel)
	}
	if m.browseSelected != 0 {
		t.Fatalf("after 3×k selection=%d, want 0 (first user turn)", m.browseSelected)
	}

	// f on the selected user turn starts a fork draft — fork is reachable.
	updated, _ = m.Update(key("f"))
	m = updated.(hubModel)
	if m.forkDraft == nil {
		t.Fatal("f on a selected user turn must start a fork draft")
	}
	if m.forkDraft.Turn != 1 {
		t.Fatalf("fork draft turn=%d, want 1", m.forkDraft.Turn)
	}

	// j walks back down (after re-entering browse on a fresh model).
	m2 := newSessionHubModel(nil)
	m2.width, m2.height = 100, 12
	m2.session.width, m2.session.height = 100, 12
	m2.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "q1", TurnIndex: 1},
		{Kind: transcript.MsgUser, Text: "q2", TurnIndex: 2},
		{Kind: transcript.MsgUser, Text: "q3", TurnIndex: 3},
	}
	m2.session.refreshViewport()
	m2.sessionView()
	m2.enterSessionBrowse(false)
	m2.sessionView()
	upd, _ := m2.Update(key("k")) // 2 -> 1
	m2 = upd.(hubModel)
	upd, _ = m2.Update(key("j")) // 1 -> 2
	m2 = upd.(hubModel)
	if m2.browseSelected != 2 {
		t.Fatalf("k then j selection=%d, want 2", m2.browseSelected)
	}
}

// TestHubModelBrowseSelectionStaysVisible verifies the cursor is auto-scrolled
// into the viewport window when it moves off the visible region.
func TestHubModelBrowseSelectionScrollsIntoView(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 100
	m.height = 12
	m.session.width = 100
	m.session.height = 12
	for i := 1; i <= 16; i++ {
		m.session.messages = append(m.session.messages, transcript.ChatMessage{Kind: transcript.MsgUser, Text: tuiBrowseMsg(i), TurnIndex: i})
	}
	m.session.refreshViewport()
	m.sessionView()
	m.enterSessionBrowse(false)
	m.sessionView()
	startOffset := m.session.viewport.YOffset
	if startOffset == 0 {
		t.Fatal("setup expected a scrolled viewport at the bottom")
	}

	// Walk the cursor up several turns; the selection must move and the viewport
	// must scroll up to follow it.
	start := m.browseSelected
	for i := 0; i < 6; i++ {
		updated, _ := m.Update(key("k"))
		m = updated.(hubModel)
	}
	if m.browseSelected != start-6 {
		t.Fatalf("6×k selection=%d, want %d", m.browseSelected, start-6)
	}
	if m.session.viewport.YOffset >= startOffset {
		t.Fatalf("viewport did not scroll up to follow the cursor: offset=%d start=%d", m.session.viewport.YOffset, startOffset)
	}
}

func tuiBrowseMsg(i int) string {
	return "request number " + string(rune('A'+i%26))
}
