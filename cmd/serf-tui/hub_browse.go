package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
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
	// ctrl+o must not leave a live focus trap armed for the next session
	// entry (spec §6.2's keypress-only invariant): defer the overlay
	// exactly like Esc does (question_overlay.go's updateQuestionKey/
	// updateReviewKey set deferred=true) rather than leaving it non-nil and
	// non-deferred. Answers are kept, matching esc-defer's rule.
	if m.questionOverlay != nil {
		m.questionOverlay.deferred = true
	}
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
		if msgrender.RenderMessage(m.session.messages[i], max(m.width, 80), false) != "" {
			return i
		}
	}
	return -1
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

// moveBrowseSelection moves the browse cursor to the next renderable message in
// the given direction (skipping ones that render empty) and scrolls the viewport
// so the newly-selected turn stays visible. Moving the selection is the only way
// to reach a user turn to fork; f forks the selected user turn. At either end the
// selection is left unchanged.
func (m *hubModel) moveBrowseSelection(direction int) {
	if len(m.session.messages) == 0 {
		m.browseSelected = -1
		return
	}
	idx := m.browseSelected
	if idx < 0 || idx >= len(m.session.messages) {
		idx = m.lastBrowseMessageIndex()
	}
	width := max(m.width, 80)
	for {
		idx += direction
		if idx < 0 || idx >= len(m.session.messages) {
			return // hit an end — keep the current selection
		}
		if msgrender.RenderMessage(m.session.messages[idx], width, false) != "" {
			m.browseSelected = idx
			m.scrollBrowseSelectionIntoView()
			return
		}
	}
}

// scrollBrowseSelectionIntoView re-syncs the body (so the ▶ cursor sits on the
// new selection) and scrolls the viewport so that cursor line is visible.
func (m *hubModel) scrollBrowseSelectionIntoView() {
	m.syncSessionViewport()
	height := m.session.viewport.Height
	if height < 1 {
		return
	}
	cursor := -1
	for i, line := range strings.Split(m.renderSessionMainBody(), "\n") {
		if strings.HasPrefix(line, msgrender.SelectionPrefix) {
			cursor = i
			break
		}
	}
	if cursor < 0 {
		return
	}
	switch top := m.session.viewport.YOffset; {
	case cursor < top:
		m.session.viewport.SetYOffset(cursor)
	case cursor >= top+height:
		m.session.viewport.SetYOffset(cursor - height + 1)
	}
}

func (m hubModel) selectedBrowseMessage() (int, transcript.ChatMessage, bool) {
	if m.browseSelected < 0 || m.browseSelected >= len(m.session.messages) {
		return -1, transcript.ChatMessage{}, false
	}
	return m.browseSelected, m.session.messages[m.browseSelected], true
}

// toggleSelectedBrowseDetail expands or collapses the detail body of just the
// selected entry — a finished tool call or a collapsed thought. A no-op when
// the selection has no collapsible body.
func (m *hubModel) toggleSelectedBrowseDetail() {
	idx, msg, ok := m.selectedBrowseMessage()
	if !ok {
		return
	}
	switch {
	case msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Done:
		m.session.messages[idx].Tool.Expanded = !msg.Tool.Expanded
	case msg.Kind == transcript.MsgReasoning && msg.Done:
		m.session.messages[idx].Expanded = !msg.Expanded
	default:
		return
	}
	m.session.refreshViewport()
}

// toggleAllBrowseDetails expands or collapses every finished detail body — tool
// calls and the model's collapsed thoughts — in one keystroke. If anything is
// still collapsed it expands all; otherwise it collapses all.
func (m *hubModel) toggleAllBrowseDetails() {
	expand := false
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Done && !msg.Tool.Expanded {
			expand = true
			break
		}
		if msg.Kind == transcript.MsgReasoning && msg.Done && !msg.Expanded {
			expand = true
			break
		}
	}
	for i := range m.session.messages {
		msg := &m.session.messages[i]
		switch {
		case msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Done:
			msg.Tool.Expanded = expand
		case msg.Kind == transcript.MsgReasoning && msg.Done:
			msg.Expanded = expand
		}
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
