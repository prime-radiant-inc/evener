package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/inputhistory"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

func (m hubModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+x" && len(m.notices) > 0 {
		m.dismissNotice()
		return m, nil
	}
	switch msg.String() {
	case "ctrl+o":
		m.returnToDashboard()
		return m, nil
	case "ctrl+c":
		if m.mode == hubModeSession && m.commandPalette == nil {
			return m.updateSessionKey(msg)
		}
		return m, tea.Quit
	}
	// Focus trap: when any overlay is open, non-escape-hatch keys are
	// consumed by the topmost overlay only (wave 9 task 9.1).
	if topmost := topmostOverlayName(m); topmost != "" && !keyAllowedThroughTrap(msg) {
		return m.dispatchOverlayKey(topmost, msg)
	}

	if m.commandPalette != nil {
		return m.updateCommandPaletteKey(msg)
	}
	if m.mode == hubModeSession {
		return m.updateSessionKey(msg)
	}
	if m.mode == hubModeSpawn {
		return m.updateSpawnKey(msg)
	}
	return m.updateDashboardKey(msg)
}

func (m hubModel) updateDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil {
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(tuipick.TextInputModal)
		m.followupModal = &modal
		if modal.Done() {
			m.followupModal = nil
		}
		return m, cmd
	}
	if m.credentialsPanel != nil {
		updated, cmd := m.credentialsPanel.Update(msg)
		panel := updated.(launchconfig.CredentialsPanel)
		m.credentialsPanel = &panel
		if panel.Done() {
			m.credentialsPanel = nil
		}
		return m, cmd
	}
	if m.launchSettingsPanel != nil {
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchconfig.LaunchSettingsPanel)
		m.launchSettingsPanel = &p
		if p.Done() {
			m.launchSettingsPanel = nil
			return m, nil
		}
		return m, cmd
	}
	if m.dashboardFilterActive {
		return m.updateHubFilterKey(msg)
	}
	rows := m.dashboardRows()
	switch msg.String() {
	case "/":
		m.openCommandPalette()
		return m, nil
	case "q":
		return m, tea.Quit
	case "r":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
	case "n":
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client, m.spawnDir)
		}
		return m, nil
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(rows)-1 {
			m.selected++
		}
	case "right", "l":
		m.setSelectedDashboardProjectExpanded(rows, true)
	case "left", "h":
		m.setSelectedDashboardProjectExpanded(rows, false)
	case "enter":
		return m.activateDashboardRow(rows)
	}
	return m, nil
}

func (m hubModel) updateHubFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dashboardFilter.Reset()
		m.dashboardFilter.Blur()
		m.dashboardFilterActive = false
		m.clampSelection()
		return m, nil
	case "enter":
		m.dashboardFilter.Blur()
		m.dashboardFilterActive = false
		m.clampSelection()
		return m.activateDashboardRow(m.dashboardRows())
	}
	var cmd tea.Cmd
	m.dashboardFilter, cmd = m.dashboardFilter.Update(msg)
	m.clampSelection()
	return m, cmd
}

func (m hubModel) activateDashboardRow(rows []hubRow) (tea.Model, tea.Cmd) {
	if len(rows) == 0 {
		return m, nil
	}
	row := rows[m.selected]
	switch row.kind {
	case hubRowLaunch:
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client, m.spawnDir)
		}
		return m, nil
	case hubRowRecentToggle:
		m.toggleDashboardRecent(row.projectKey)
		m.clampSelection()
		return m, nil
	case hubRowProject:
		m.toggleDashboardProject(row.projectKey)
		m.clampSelection()
		return m, nil
	}
	if m.client == nil {
		return m, nil
	}
	return m, fetchHubSession(m.client, row.ref)
}

func (m *hubModel) enterHubFilter() {
	m.dashboardFilterActive = true
	m.dashboardFilter.Focus()
	m.clampSelection()
}

func (m *hubModel) openCommandPalette() {
	width := m.width
	if width <= 0 {
		width = 100
	}
	rows := m.dashboardRows()
	if m.mode == hubModeSession {
		rows = nil
	}
	palette := newCommandPalette("Command palette", commandPaletteEntriesForRows(m.mode, m.detail.Capabilities, rows), width)
	m.commandPalette = &palette
}

func (m hubModel) updateCommandPaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.commandPalette == nil {
		return m, nil
	}
	updated, cmd := m.commandPalette.Update(msg)
	palette := updated.(commandPalette)
	m.commandPalette = &palette
	if !palette.panel.Done() {
		return m, cmd
	}
	m.commandPalette = nil
	if palette.panel.Cancelled() {
		return m, cmd
	}
	entry, ok := palette.selectedEntry()
	if !ok {
		return m, cmd
	}
	switch entry.Kind {
	case commandPaletteCommand:
		return m.runCommandPaletteCommand(entry.Command)
	case commandPaletteProject:
		m.focusDashboardProject(entry.ProjectKey)
		return m, nil
	case commandPaletteSession:
		if m.client == nil {
			return m, nil
		}
		return m, fetchHubSession(m.client, entry.Ref)
	default:
		return m, cmd
	}
}

func (m hubModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == hubModeSession && (m.session.scrollMode || m.transcriptView != nil || mouseWheelScrollsTranscript(msg)) {
		if m.transcriptView == nil && !m.session.scrollMode {
			m.enterSessionBrowse(false)
		}
		var cmd tea.Cmd
		m.session.viewport, cmd = m.session.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func mouseWheelScrollsTranscript(msg tea.MouseMsg) bool {
	if msg.Action != tea.MouseActionPress {
		return false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		return true
	default:
		return false
	}
}

func (m hubModel) updateSessionBrowseComposerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if m.session.input.Value() == "" && m.session.historyIdx < 0 && len(m.session.history) == 0 {
			m.session.viewport.ScrollUp(1)
			return m, nil
		}
		if len(m.session.history) > 0 {
			if m.session.historyIdx >= 0 {
				if m.session.historyIdx > 0 {
					m.session.historyIdx--
				}
			} else if m.session.input.Value() == "" {
				m.session.historyDraft = m.session.input.Value()
				m.session.historyIdx = len(m.session.history) - 1
			} else {
				return m, nil
			}
			m.session.setInputValue(inputhistory.UnescapeHistory(m.session.history[m.session.historyIdx]))
			return m, nil
		}
	case "down":
		if m.session.input.Value() == "" && m.session.historyIdx < 0 && len(m.session.history) == 0 {
			m.session.viewport.ScrollDown(1)
			return m, nil
		}
		if m.session.historyIdx >= 0 {
			if m.session.historyIdx < len(m.session.history)-1 {
				m.session.historyIdx++
				m.session.setInputValue(inputhistory.UnescapeHistory(m.session.history[m.session.historyIdx]))
			} else {
				m.session.historyIdx = -1
				m.session.setInputValue(m.session.historyDraft)
				m.session.historyDraft = ""
			}
			return m, nil
		}
	}
	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSessionInputFrom(prevHeight)
	return m, cmd
}

func (m hubModel) runCommandPaletteCommand(command string) (tea.Model, tea.Cmd) {
	definition, ok := hubCommandByName(command)
	if !ok {
		return m, nil
	}
	available, _ := hubCommandAvailable(definition, hubCommandContext{mode: m.mode, caps: m.detail.Capabilities})
	if !available {
		return m, nil
	}
	cmd := runHubCommandDefinition(&m, definition, "")
	return m, cmd
}
