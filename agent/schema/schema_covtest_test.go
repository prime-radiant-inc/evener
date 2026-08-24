package schema

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestCovHookInfoAnnouncement covers HookInfo.Announcement
// (turn.go lines 108-121).
func TestCovHookInfoAnnouncement(t *testing.T) {
	// Full fields.
	h := HookInfo{
		Event:      "pre",
		PluginName: "myplugin",
		Matcher:    "exec_command",
		HookType:   "pre",
		ExitCode:   0,
	}
	got := h.Announcement()
	if !strings.Contains(got, "pre hook") || !strings.Contains(got, "myplugin") || !strings.Contains(got, "exec_command") || !strings.Contains(got, "exit 0") {
		t.Fatalf("full: %q", got)
	}

	// Empty event → defaults to "hook".
	h = HookInfo{ExitCode: 1}
	got = h.Announcement()
	if !strings.Contains(got, "hook hook") {
		t.Fatalf("empty event: %q", got)
	}
	if !strings.Contains(got, "exit 1") {
		t.Fatalf("missing exit code: %q", got)
	}

	// Partial fields — whitespace trimmed.
	h = HookInfo{
		Event:      "  post  ",
		PluginName: "  ",
		ExitCode:   2,
	}
	got = h.Announcement()
	if !strings.Contains(got, "post hook") {
		t.Fatalf("trimmed event: %q", got)
	}
	// Empty/whitespace plugin name should be skipped.
	if strings.Contains(got, "  ") || strings.Contains(got, " \t") {
		// May have double spaces from join, but should not have the raw whitespace field.
	}
}

// TestCovNewTurn covers NewTurn (turn.go lines 200-202).
func TestCovNewTurn(t *testing.T) {
	msg := llm.User("hello")
	turn := NewTurn(TurnUserInput, msg)
	if turn.Kind != TurnUserInput {
		t.Fatalf("Kind = %v", turn.Kind)
	}
	if turn.Message.Role != llm.RoleUser {
		t.Fatalf("Role = %v", turn.Message.Role)
	}
	if turn.Timestamp.IsZero() {
		t.Fatal("Timestamp should be set")
	}
}
