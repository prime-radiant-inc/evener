package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// newCredentialsPanelForTest returns a credentials panel suitable for
// focus-trap testing — it has no loading state that would block key handling.
func newCredentialsPanelForTest() *launchconfig.CredentialsPanel {
	p := launchconfig.CredentialsPanel{}
	return &p
}

func TestTopmostOverlayNameEmpty(t *testing.T) {
	m := newHubModel(nil, "")
	if got := topmostOverlayName(m); got != "" {
		t.Errorf("expected empty overlay name, got %q", got)
	}
}

func TestTopmostOverlayNameCredentials(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	if got := topmostOverlayName(m); got != "credentials" {
		t.Errorf("expected credentials, got %q", got)
	}
}

func TestTopmostOverlayNamePlugins(t *testing.T) {
	m := newHubModel(nil, "")
	panel := launchconfig.NewPluginsPanel()
	m.pluginsPanel = &panel
	if got := topmostOverlayName(m); got != "plugins" {
		t.Errorf("expected plugins, got %q", got)
	}
}

func TestTopmostOverlayNameFollowupTakesPrecedence(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	modal := tuipick.NewTextInputModal("test", "")
	m.followupModal = &modal
	if got := topmostOverlayName(m); got != "followup" {
		t.Errorf("expected followup (topmost), got %q", got)
	}
}

func TestKeyAllowedThroughTrap_Esc(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	if !keyAllowedThroughTrap(msg) {
		t.Errorf("esc should be allowed through trap")
	}
}

func TestKeyAllowedThroughTrap_CtrlO(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyCtrlO}
	if !keyAllowedThroughTrap(msg) {
		t.Errorf("ctrl+o should be allowed through trap")
	}
}

func TestKeyAllowedThroughTrap_CtrlP_Rejected(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyCtrlP}
	if keyAllowedThroughTrap(msg) {
		t.Errorf("ctrl+p should NOT be allowed through trap")
	}
}

func TestCmdPRejectedWhenCredentialsOpen(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	before := m.commandPalette
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	after := updated.(hubModel).commandPalette
	if before == nil && after != nil {
		t.Errorf("ctrl+P should be trapped while credentials panel open; palette opened anyway")
	}
}

func TestEscClosesTopmostOverlayOnly(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(hubModel).credentialsPanel != nil {
		t.Errorf("esc should close credentials panel")
	}
}

func TestCtrlOEscapesAllOverlays(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	finalM := updated.(hubModel)
	if finalM.credentialsPanel != nil {
		t.Errorf("ctrl+o should close credentials panel")
	}
	if finalM.mode != hubModeDashboard {
		t.Errorf("ctrl+o should return to dashboard; got mode %v", finalM.mode)
	}
}

func TestSlashRejectedWhenCredentialsOpen(t *testing.T) {
	m := newHubModel(nil, "")
	m.credentialsPanel = newCredentialsPanelForTest()
	before := m.commandPalette
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	after := updated.(hubModel).commandPalette
	if before == nil && after != nil {
		t.Errorf("/ should be trapped while credentials panel open; command palette opened anyway")
	}
}

func TestEscClosesPluginsOverlay(t *testing.T) {
	m := newHubModel(nil, "")
	panel := launchconfig.NewPluginsPanel()
	m.pluginsPanel = &panel
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(hubModel).pluginsPanel != nil {
		t.Errorf("esc should close the plugins panel")
	}
}

func TestCtrlOEscapesPluginsOverlay(t *testing.T) {
	m := newHubModel(nil, "")
	panel := launchconfig.NewPluginsPanel()
	m.pluginsPanel = &panel
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	finalM := updated.(hubModel)
	if finalM.pluginsPanel != nil {
		t.Errorf("ctrl+o should close the plugins panel")
	}
	if finalM.mode != hubModeDashboard {
		t.Errorf("ctrl+o should return to dashboard; got mode %v", finalM.mode)
	}
}

func TestCmdPRejectedWhenPluginsOpen(t *testing.T) {
	m := newHubModel(nil, "")
	panel := launchconfig.NewPluginsPanel()
	m.pluginsPanel = &panel
	before := m.commandPalette
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	after := updated.(hubModel).commandPalette
	if before == nil && after != nil {
		t.Errorf("ctrl+P should be trapped while plugins panel open; palette opened anyway")
	}
}
