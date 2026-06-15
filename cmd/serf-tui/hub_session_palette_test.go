package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHubModelSessionPaletteKeepsSelectionVisibleWhenWindowed is the regression
// test for the asymmetry the overlay-bound fix left behind: the session-mode
// command palette must window itself like the dashboard one, so its selection
// stays visible on a short pane instead of being trimmed off the bottom by
// AppShell's hard bound.
func TestHubModelSessionPaletteKeepsSelectionVisibleWhenWindowed(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 80
	m.session.width = 80
	m.height = 14
	m.session.height = 14
	m.openCommandPalette()

	// Drive the cursor to the last entry; on a short pane the windowed overlay
	// must still render the selected row.
	filtered := m.commandPalette.panel.Filtered()
	for i := 0; i < len(filtered); i++ {
		updated, _ := m.commandPalette.Update(tea.KeyMsg{Type: tea.KeyDown})
		palette := updated.(commandPalette)
		m.commandPalette = &palette
	}
	selected := filtered[len(filtered)-1].Label

	got := m.View()
	if !strings.Contains(got, "> ") {
		t.Fatalf("windowed session palette dropped the selection cursor:\n%s", got)
	}
	if !strings.Contains(got, selected) {
		t.Fatalf("windowed session palette dropped selected entry %q:\n%s", selected, got)
	}
	if lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n"); len(lines) > m.height {
		t.Fatalf("session palette rendered %d lines, exceeds height %d:\n%s", len(lines), m.height, got)
	}
}
