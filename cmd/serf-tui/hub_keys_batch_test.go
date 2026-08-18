package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A pty delivers rapid keystrokes as one read burst under CPU contention
// (tmux coalesces separate send-keys writes when the pane's reader lags; a
// human typing fast does the same), and bubbletea reports every printable
// rune of one read as a single KeyMsg. Key dispatch must apply those runes
// individually — switching on msg.String() alone silently drops "/ops" on
// the dashboard and types "kk" into the composer instead of moving the
// transcript selection (kata fazd: two CI failures with exactly those
// panes).

func TestBatchedRunesApplyIndividuallyOnDashboard(t *testing.T) {
	m := newHubModel(nil, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/ops")})
	got := updated.(hubModel)
	if got.commandPalette == nil {
		t.Fatalf("batched '/ops' must open the command palette and filter it; nothing happened")
	}
	if view := got.commandPalette.ViewWithMaxHeight(40); !strings.Contains(view, "ops") {
		t.Fatalf("batched '/ops' opened the palette but did not filter it to 'ops':\n%s", view)
	}
}

func TestBatchedSlashCommandStaysComposerText(t *testing.T) {
	m := newSessionHubModel(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/interrupt")})
	got := updated.(hubModel)
	if got.commandPalette != nil {
		t.Fatalf("a coalesced '/interrupt' typed at the composer must stay composer text; it opened the palette")
	}
	if got.session.input.Value() != "/interrupt" {
		t.Fatalf("composer should hold the typed command, got %q", got.session.input.Value())
	}
}

func TestBatchedPasteStaysOneMessage(t *testing.T) {
	m := newHubModel(nil, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/ops"), Paste: true})
	got := updated.(hubModel)
	if got.commandPalette != nil {
		t.Fatalf("a bracketed paste of '/ops' is text, not keystrokes; it must not open the palette")
	}
}
