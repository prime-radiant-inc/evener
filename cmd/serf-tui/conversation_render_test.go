package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func TestUserMessageGetsAccentBar(t *testing.T) {
	msg := transcript.ChatMessage{Kind: transcript.MsgUser, Text: "Hello"}
	got := msgrender.RenderMessage(msg, 80, false)
	if !strings.Contains(got, "┃") {
		t.Errorf("user message should carry ┃ bar: %q", got)
	}
}

func TestAssistantMessageGetsStateBar(t *testing.T) {
	msg := transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: "Working on it"}
	got := msgrender.RenderMessage(msg, 80, false)
	if !strings.Contains(got, "▍") {
		t.Errorf("assistant message should carry ▍ bar: %q", got)
	}
}

func TestScrollBrowseFocusedTurnHasDoubleBar(t *testing.T) {
	msg := transcript.ChatMessage{Kind: transcript.MsgUser, Text: "X"}
	got := msgrender.RenderMessage(msg, 80, true)
	if strings.Count(got, "┃") < 2 {
		t.Errorf("focused user turn should have double-bar: %q", got)
	}
}

func TestForkDraftHasSectionDivider(t *testing.T) {
	header := forkDraftHeader("feat/widget", 1, 80)
	// tuiprim.SectionDivider uppercases the left label, so check case-insensitively.
	lc := strings.ToLower(header)
	if !strings.Contains(lc, "fork draft") || !strings.Contains(header, "feat/widget") {
		t.Errorf("fork draft header missing pieces: %q", header)
	}
	if !strings.Contains(header, "─") {
		t.Errorf("fork draft header should use SectionDivider: %q", header)
	}
}
