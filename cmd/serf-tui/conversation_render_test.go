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
