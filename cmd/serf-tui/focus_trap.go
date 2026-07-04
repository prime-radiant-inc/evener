package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// topmostOverlayName returns the name of the most-recently-opened
// overlay on hubModel, or "" if none. Order matters: most-recent first.
func topmostOverlayName(m hubModel) string {
	// Gated to session mode: the question overlay is only ever opened
	// (ctrl+q, toggleAskOverlay) while viewing a session, but Deferred
	// leaves the pointer set even after the user returns to the dashboard
	// (spec: esc/navigating away keeps answers, it doesn't destroy them).
	// Without this gate a stale, non-deferred overlay from a previous
	// session visit would trap keys outside session mode.
	if m.mode == hubModeSession && m.questionOverlay != nil && !m.questionOverlay.Deferred() {
		return "question-overlay"
	}
	if m.followupModal != nil {
		return "followup"
	}
	if m.launchOverridesModal != nil {
		return "launch-overrides"
	}
	if m.credentialsPanel != nil {
		return "credentials"
	}
	if m.launchSettingsPanel != nil {
		return "launch-settings"
	}
	if m.sessionPanel != nil {
		return "session-panel"
	}
	if m.commandPalette != nil {
		return "command-palette"
	}
	if m.sessionModelPicker != nil || m.sessionThemePicker != nil || m.sessionTranscriptPicker != nil {
		return "picker"
	}
	return ""
}

// keyAllowedThroughTrap returns true ONLY for the two global escape
// hatches: esc (closes topmost overlay) and ctrl+o (escape all + dashboard).
func keyAllowedThroughTrap(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlO:
		return true
	}
	return false
}

// dispatchOverlayKey routes a key message to the named topmost overlay.
// If the overlay doesn't handle the key, it is dropped — no passthrough to
// hub model. Esc and ctrl+o are handled before this function is called.
func (m hubModel) dispatchOverlayKey(name string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch name {
	case "question-overlay":
		// Mirrors the "picker" case below: updateSessionKey's own early
		// return isolates the overlay's key handling (and is also reached
		// directly for Esc, which bypasses the trap entirely).
		return m.updateSessionKey(msg)

	case "followup":
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(tuipick.TextInputModal)
		m.followupModal = &modal
		if modal.Done() {
			m.followupModal = nil
		}
		return m, cmd

	case "launch-overrides":
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchconfig.LaunchOverridesModal)
		m.launchOverridesModal = &p
		if p.Done() {
			m.launchOverridesModal = nil
		}
		return m, cmd

	case "credentials":
		updated, cmd := m.credentialsPanel.Update(msg)
		panel := updated.(launchconfig.CredentialsPanel)
		m.credentialsPanel = &panel
		if panel.Done() {
			m.credentialsPanel = nil
		}
		return m, cmd

	case "launch-settings":
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchconfig.LaunchSettingsPanel)
		m.launchSettingsPanel = &p
		if p.Done() {
			m.launchSettingsPanel = nil
		}
		return m, cmd

	case "session-panel":
		// sessionPanel has no Update; only esc closes it (routed via
		// updateSessionKey which handles the esc + refreshViewport pattern).
		// Non-esc keys fall through silently — the panel consumes them.
		return m, nil

	case "command-palette":
		// Delegate to the full command-palette handler which owns post-selection
		// side effects (running commands, switching sessions, opening spawn).
		return m.updateCommandPaletteKey(msg)

	case "picker":
		// Session pickers have complex post-selection side effects (model
		// switching, theme apply, transcript fetch). Delegate to
		// updateSessionKey which already isolates picker key handling in
		// its early-return guards. The trap has already ensured no other
		// session key handler will run.
		return m.updateSessionKey(msg)
	}
	return m, nil
}
