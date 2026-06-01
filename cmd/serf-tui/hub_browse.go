package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/internal/appwire"
)

func (m *hubModel) enterSessionBrowse(pageUp bool) {
	wasComposing := !m.session.scrollMode && m.transcriptView == nil
	m.session.scrollMode = true
	m.session.focusedToolIdx = -1
	m.session.input.Focus()
	if m.browseSelected < 0 || m.browseSelected >= len(m.session.messages) {
		m.browseSelected = m.lastBrowseMessageIndex()
	}
	// Re-sync the viewport before any scroll work below — flipping to
	// browse mode changes the chrome composition (composer panel vs.
	// browse-mode footer), so bodyHeight and content differ from the
	// compose-mode state the viewport was last synced to. Without this,
	// the GotoBottom / PgUp call would operate against stale geometry.
	m.syncSessionViewport()
	if wasComposing {
		m.session.viewport.GotoBottom()
	}
	if pageUp {
		m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
}

func (m *hubModel) exitSessionBrowse() {
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.browseSelected = -1
	m.session.input.Focus()
}

func (m *hubModel) returnToDashboard() {
	if m.mode == hubModeSpawn {
		m.resetSpawnForm()
	}
	m.commandPalette = nil
	m.sessionThemePicker = nil
	m.sessionModelPicker = nil
	m.sessionTranscriptPicker = nil
	m.transcriptTargets = nil
	m.transcriptView = nil
	m.forkDraft = nil
	m.spawnModelPicker = nil
	m.credentialsPanel = nil
	m.launchSettingsPanel = nil
	m.followupModal = nil
	m.launchOverridesModal = nil
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.browseSelected = -1
	m.authLoginProvider = ""
	m.authLoginFlowID = ""
	m.clearSessionQueue()
	m.mode = hubModeDashboard
	m.clampSelection()
}

func (m hubModel) lastBrowseMessageIndex() int {
	for i := len(m.session.messages) - 1; i >= 0; i-- {
		if renderMessage(m.session.messages[i], max(m.width, 80), false) != "" {
			return i
		}
	}
	return -1
}

func (m *hubModel) moveBrowseSelection(delta int) {
	if len(m.session.messages) == 0 {
		m.browseSelected = -1
		return
	}
	idx := m.browseSelected
	if idx < 0 || idx >= len(m.session.messages) {
		idx = m.lastBrowseMessageIndex()
	}
	for {
		idx += delta
		if idx < 0 || idx >= len(m.session.messages) {
			break
		}
		if renderMessage(m.session.messages[idx], max(m.width, 80), false) != "" {
			m.browseSelected = idx
			m.session.scrollToMessage(idx)
			return
		}
	}
	m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
	if delta > 0 {
		m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

func (m *hubModel) moveBrowsePage(direction int) {
	step := m.session.viewport.Height
	if step < 1 {
		step = 5
	}
	if direction < 0 {
		m.session.viewport.ScrollUp(step)
		return
	}
	m.session.viewport.ScrollDown(step)
}

func (m hubModel) selectedBrowseMessage() (int, transcript.ChatMessage, bool) {
	if m.browseSelected < 0 || m.browseSelected >= len(m.session.messages) {
		return -1, transcript.ChatMessage{}, false
	}
	return m.browseSelected, m.session.messages[m.browseSelected], true
}

func (m *hubModel) toggleSelectedBrowseEntry() {
	idx, msg, ok := m.selectedBrowseMessage()
	if !ok || msg.Kind != transcript.MsgTool || msg.Tool == nil || !msg.Tool.Done {
		return
	}
	m.setSelectedBrowseEntryExpanded(!msg.Tool.Expanded)
	m.session.scrollToMessage(idx)
}

func (m *hubModel) setSelectedBrowseEntryExpanded(expanded bool) {
	idx, msg, ok := m.selectedBrowseMessage()
	if !ok || msg.Kind != transcript.MsgTool || msg.Tool == nil || !msg.Tool.Done {
		return
	}
	m.session.messages[idx].Tool.Expanded = expanded
	m.session.refreshViewport()
}

func (m *hubModel) toggleAllBrowseToolEntries() {
	expand := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Done && !msg.Tool.Expanded {
			expand = true
			break
		}
	}
	for i := range m.session.messages {
		msg := &m.session.messages[i]
		if msg.Kind != transcript.MsgTool || msg.Tool == nil || !msg.Tool.Done {
			continue
		}
		msg.Tool.Expanded = expand
	}
	m.session.refreshViewport()
}

func (m *hubModel) startForkDraft() {
	_, msg, ok := m.selectedBrowseMessage()
	if !ok {
		m.addSessionSystem("Select a user turn to fork.")
		return
	}
	if msg.Kind != transcript.MsgUser {
		m.addSessionSystem("Select a user turn to fork.")
		return
	}
	if msg.TurnIndex <= 0 {
		m.addSessionSystem("fork requires persisted transcript turn identity.")
		return
	}
	if !m.detail.Capabilities.Fork {
		m.addSessionSystem("Fork is not available for this session.")
		return
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return
	}
	m.forkDraft = &hubForkDraft{
		Ref:          ref,
		Turn:         msg.TurnIndex,
		OriginalText: msg.Text,
		Label:        "original before fork",
	}
	m.exitSessionBrowse()
	m.session.setInputValue(msg.Text)
	m.addSessionSystem(fmt.Sprintf("Fork draft for turn %d. Edit the input, press enter to fork, or esc to cancel.", msg.TurnIndex))
}

func (m *hubModel) addSessionSystem(text string) {
	m.session.messages = append(m.session.messages, transcript.ChatMessage{Kind: transcript.MsgSystem, Text: text})
	m.session.refreshViewport()
}

func (m *hubModel) addAuthErrorNotice(title string, err error) {
	m.addNotice(noticePanel{
		Title:      title,
		Category:   "auth",
		Summary:    "OpenAI authentication failed.",
		Source:     m.sourceLabelForNotice(),
		Reason:     err.Error(),
		NextAction: "Retry /auth openai or check Hub auth configuration.",
	})
}

func (m *hubModel) recordSessionError(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.sessionStatusError = text
	m.addSessionSystem(text)
}

func (m *hubModel) clearSessionError() {
	m.sessionStatusError = ""
}

func (m *hubModel) removeTrailingSessionSystem(text string) {
	if len(m.session.messages) == 0 {
		return
	}
	last := m.session.messages[len(m.session.messages)-1]
	if last.Kind != transcript.MsgSystem || last.Text != text {
		return
	}
	m.session.messages = m.session.messages[:len(m.session.messages)-1]
	m.session.refreshViewport()
}

func (m *hubModel) addSessionSystemOnce(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(m.session.messages) > 0 {
		last := m.session.messages[len(m.session.messages)-1]
		if last.Kind == transcript.MsgSystem && last.Text == text {
			return
		}
	}
	m.addSessionSystem(text)
}

func (m hubModel) currentRef() (appwire.Ref, bool) {
	ref, err := appwire.ParseRef(m.detail.Ref)
	if err != nil {
		return appwire.Ref{}, false
	}
	return ref, true
}

func (m hubModel) matchesAsyncSessionRef(ref string) bool {
	return m.mode == hubModeSession && strings.TrimSpace(ref) != "" && strings.TrimSpace(m.detail.Ref) == strings.TrimSpace(ref)
}
