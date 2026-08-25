package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// ---- topmostOverlayName: more overlay orderings ------------------------------

func TestCovTopmostOverlayName_LaunchOverrides(t *testing.T) {
	m := newHubModel(nil, "")
	m.launchOverridesModal = &launchconfig.LaunchOverridesModal{}
	if got := topmostOverlayName(m); got != "launch-overrides" {
		t.Fatalf("launch-overrides = %q, want launch-overrides", got)
	}
}

func TestCovTopmostOverlayName_LaunchSettings(t *testing.T) {
	m := newHubModel(nil, "")
	m.launchSettingsPanel = &launchconfig.LaunchSettingsPanel{}
	if got := topmostOverlayName(m); got != "launch-settings" {
		t.Fatalf("launch-settings = %q, want launch-settings", got)
	}
}

func TestCovTopmostOverlayName_SessionPanel(t *testing.T) {
	m := newHubModel(nil, "")
	m.sessionPanel = &hubSessionPanel{}
	if got := topmostOverlayName(m); got != "session-panel" {
		t.Fatalf("session-panel = %q, want session-panel", got)
	}
}

func TestCovTopmostOverlayName_CommandPalette(t *testing.T) {
	m := newHubModel(nil, "")
	entries := []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}
	p := newCommandPalette("Test", entries, 80)
	m.commandPalette = &p
	if got := topmostOverlayName(m); got != "command-palette" {
		t.Fatalf("command-palette = %q, want command-palette", got)
	}
}

func TestCovTopmostOverlayName_Picker(t *testing.T) {
	m := newHubModel(nil, "")
	picker := tuipick.NewModelPicker(nil, "", 80)
	m.sessionModelPicker = &picker
	if got := topmostOverlayName(m); got != "picker" {
		t.Fatalf("picker = %q, want picker", got)
	}
}

func TestCovTopmostOverlayName_QuestionOverlayDeferred(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	overlay := newQuestionOverlay("ref", nil, 80)
	overlay.deferred = true
	m.questionOverlay = overlay
	if got := topmostOverlayName(m); got != "" {
		t.Fatalf("deferred question overlay topmost name = %q, want empty", got)
	}
}

func TestCovTopmostOverlayName_QuestionOverlayNonSessionMode(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeDashboard
	overlay := newQuestionOverlay("ref", nil, 80)
	m.questionOverlay = overlay
	if got := topmostOverlayName(m); got != "" {
		t.Fatalf("dashboard question overlay topmost name = %q, want empty", got)
	}
}

func TestCovTopmostOverlayName_QuestionOverlayActive(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	overlay := newQuestionOverlay("ref", []askQuestion{twoOptionQuestion("Q1", false, "")}, 80)
	m.questionOverlay = overlay
	if got := topmostOverlayName(m); got != "question-overlay" {
		t.Fatalf("active question overlay = %q, want question-overlay", got)
	}
}

// ---- keyAllowedThroughTrap: more cases ---------------------------------------

func TestCovKeyAllowedThroughTrap_RunesRejected(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	if keyAllowedThroughTrap(msg) {
		t.Fatalf("runes should NOT be allowed through trap")
	}
}

func TestCovKeyAllowedThroughTrap_EnterRejected(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	if keyAllowedThroughTrap(msg) {
		t.Fatalf("enter should NOT be allowed through trap")
	}
}

// ---- dispatchOverlayKey: various overlay names -------------------------------

func TestCovDispatchOverlayKey_UnknownName(t *testing.T) {
	m := hubModel{
		mode:     hubModeSession,
		width:    91,
		height:   42,
		selected: 3,
		notices:  []noticePanel{{Title: "keep me"}},
	}
	updated, cmd := m.dispatchOverlayKey("unknown-overlay", tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("unknown overlay returned command %T, want nil", cmd)
	}
	want := hubModel{
		mode:     hubModeSession,
		width:    91,
		height:   42,
		selected: 3,
		notices:  []noticePanel{{Title: "keep me"}},
	}
	if got := updated.(hubModel); !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown overlay changed model:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCovDispatchOverlayKey_SessionPanelNonEsc(t *testing.T) {
	m := newHubModel(nil, "")
	m.sessionPanel = &hubSessionPanel{}
	updated, cmd := m.dispatchOverlayKey("session-panel", tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(hubModel)
	if got.sessionPanel != m.sessionPanel {
		t.Fatalf("non-esc key replaced or closed session panel")
	}
	if cmd != nil {
		t.Fatalf("non-esc session-panel key returned command %T, want nil", cmd)
	}
}

// hubSessionPanel is a lightweight panel type used in tests. The real one
// exists in the tui package; if it's a different name, we use a nil-safe
// pointer.

// ---- dispatchOverlayKey: picker delegates to updateSessionKey ---------------

func TestCovDispatchOverlayKey_Picker(t *testing.T) {
	m := newSessionHubModel(nil)
	picker := tuipick.NewModelPicker(nil, "", 80)
	m.sessionModelPicker = &picker
	updated, cmd := m.dispatchOverlayKey("picker", tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(hubModel)
	if got.sessionModelPicker != nil {
		t.Fatalf("esc through picker trap left picker open")
	}
	if cmd != nil {
		t.Fatalf("closing picker returned command %T, want nil", cmd)
	}
}
