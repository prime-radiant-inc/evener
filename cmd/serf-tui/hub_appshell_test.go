package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHubModelAppShellKeepsDashboardFooterUnderPaletteOverlay(t *testing.T) {
	m := sampleHubModel(100)
	m.openCommandPalette()

	got := m.View()
	requireOrderedText(t, got, "serf live", "Command palette", "ctrl+o dashboard")
}

func TestHubModelAppShellKeepsProjectFooterUnderPaletteOverlay(t *testing.T) {
	m := sampleHubModel(100)
	m.openProject("serf")
	m.openCommandPalette()

	got := m.View()
	requireOrderedText(t, got, "serf / project / serf", "Command palette", "ctrl+o dashboard")
}

func TestHubModelAppShellSessionTopBarAndComposerRegion(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "Ready for shell work."}}

	got := m.View()
	requireOrderedText(t, got, "serf / session / send task", "Ready for shell work.", "message", "> ")
}

func TestHubModelCtrlOReturnsDashboardFromCommandPaletteOverlay(t *testing.T) {
	m := sampleHubModel(100)
	m.openProject("serf")
	m.openCommandPalette()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd != nil {
		t.Fatal("ctrl+o dashboard return should be synchronous")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if got.commandPalette != nil {
		t.Fatalf("command palette stayed open after ctrl+o")
	}
}

func TestHubModelCtrlOClearsSessionOverlay(t *testing.T) {
	m := newSessionHubModel(nil)
	picker := newModelPicker([]modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}, "", 100)
	m.sessionModelPicker = &picker

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd != nil {
		t.Fatal("ctrl+o dashboard return should be synchronous")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if got.sessionModelPicker != nil {
		t.Fatalf("session model picker stayed open after ctrl+o")
	}
}

func TestActionBarWrapsBeforeDroppingDashboardHint(t *testing.T) {
	got := actionBarForWidth(60, "↑/↓ select", "enter open", "p project", "n new", "/ palette", "ctrl+o dashboard", "q quit")

	if !strings.Contains(got, "ctrl+o dashboard") {
		t.Fatalf("wrapped action bar dropped dashboard hint:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 60 {
			t.Fatalf("action bar line too wide (%d): %q\n%s", len([]rune(line)), line, got)
		}
	}
}

func requireOrderedText(t *testing.T, got string, parts ...string) {
	t.Helper()
	pos := -1
	for _, part := range parts {
		next := strings.Index(got, part)
		if next < 0 {
			t.Fatalf("view missing %q:\n%s", part, got)
		}
		if next < pos {
			t.Fatalf("view rendered %q before prior parts %v:\n%s", part, parts, got)
		}
		pos = next
	}
}
