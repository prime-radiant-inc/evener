package tui

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

// bubbletea hands hub_keys.go a KeyMsg whose Runes are already decoded code
// points (key.go's detectOneMsg runs utf8.DecodeRune per byte before ever
// building the batch), so a multi-byte rune can never straddle a replay
// boundary the way it could if replayKeyBurst iterated raw bytes instead of
// msg.Runes. This pins that guarantee end to end: two multi-byte runes
// (☕ U+2615, 3 bytes; ✈ U+2708, 3 bytes) replayed one at a time into the
// command palette filter must come out byte-for-byte identical to typing
// them as a single rune, not mangled or interleaved with the ASCII around
// them.
func TestBatchedRunesWithMultibyteRunesPreserveEncoding(t *testing.T) {
	m := newHubModel(nil, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/☕✈ops")})
	got := updated.(hubModel)
	if got.commandPalette == nil {
		t.Fatalf("batched '/☕✈ops' must open the command palette; nothing happened")
	}
	if filter := got.commandPalette.ViewWithMaxHeight(40); !strings.Contains(filter, "Filter: ☕✈ops") {
		t.Fatalf("batched multi-byte burst corrupted the filter text:\n%s", filter)
	}
}
