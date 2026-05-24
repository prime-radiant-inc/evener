package main

import (
	"strings"
	"testing"
)

func TestUserMessageGetsAccentBar(t *testing.T) {
	msg := chatMessage{Kind: msgUser, Text: "Hello"}
	got := renderMessage(msg, 80, false)
	if !strings.Contains(got, "┃") {
		t.Errorf("user message should carry ┃ bar: %q", got)
	}
}

func TestAssistantMessageGetsStateBar(t *testing.T) {
	msg := chatMessage{Kind: msgAssistant, Text: "Working on it"}
	got := renderMessage(msg, 80, false)
	if !strings.Contains(got, "▍") {
		t.Errorf("assistant message should carry ▍ bar: %q", got)
	}
}

func TestScrollBrowseFocusedTurnHasDoubleBar(t *testing.T) {
	msg := chatMessage{Kind: msgUser, Text: "X"}
	got := renderMessage(msg, 80, true)
	if strings.Count(got, "┃") < 2 {
		t.Errorf("focused user turn should have double-bar: %q", got)
	}
}

func TestForkDraftHasSectionDivider(t *testing.T) {
	header := forkDraftHeader("feat/widget", 1, 80)
	// SectionDivider uppercases the left label, so check case-insensitively.
	lc := strings.ToLower(header)
	if !strings.Contains(lc, "fork draft") || !strings.Contains(header, "feat/widget") {
		t.Errorf("fork draft header missing pieces: %q", header)
	}
	if !strings.Contains(header, "─") {
		t.Errorf("fork draft header should use SectionDivider: %q", header)
	}
}
