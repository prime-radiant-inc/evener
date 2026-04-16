package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/server"
)

const transcriptRefreshInterval = 750 * time.Millisecond

type subagentUI struct {
	status    string
	turnsUsed int
}

type transcriptViewState struct {
	sessionID string
	title     string
	root      bool
	messages  []chatMessage
	readErr   string
}

func (t *transcriptViewState) banner() string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("Viewing %s. Press esc to return to chat.", t.title)
}

func scheduleTranscriptRefresh() tea.Cmd {
	return tea.Tick(transcriptRefreshInterval, func(time.Time) tea.Msg {
		return transcriptRefreshMsg{}
	})
}

func transcriptFilePath(stateDir, sessionID string) string {
	return filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
}

func loadTranscriptMessages(stateDir, sessionID string) ([]chatMessage, error) {
	_, entries, _, err := agent.ReadTranscript(transcriptFilePath(stateDir, sessionID))
	if err != nil {
		return nil, err
	}
	return historyToMessages(agent.ResumeHistory(entries)), nil
}

func pickerDisplay(items []modelPickerItem, selectedID string) string {
	for _, item := range items {
		if item.id == selectedID {
			return item.display
		}
	}
	return selectedID
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func renderSubagentLabel(id string, info subagentUI) string {
	label := fmt.Sprintf("subagent %s", shortSessionID(id))
	if info.status != "" {
		label += fmt.Sprintf(" (%s", info.status)
		if info.turnsUsed > 0 {
			label += fmt.Sprintf(", %d turns", info.turnsUsed)
		}
		label += ")"
	}
	return label
}

func (m *model) trackSubagent(info server.SubagentStatusInfo) {
	if info.ID == "" {
		return
	}
	if m.observedSubagents == nil {
		m.observedSubagents = make(map[string]subagentUI)
	}
	m.observedSubagents[info.ID] = subagentUI{
		status:    info.Status,
		turnsUsed: info.TurnsUsed,
	}
}

func (m *model) buildTranscriptPickerItems(info server.StatusInfo) []modelPickerItem {
	rootID := info.SessionID
	if rootID == "" {
		rootID = m.sessionID
	}
	if rootID == "" {
		return nil
	}

	items := []modelPickerItem{{
		id:      rootID,
		display: "main session (live)",
	}}

	subagents := make(map[string]subagentUI, len(m.observedSubagents))
	for id, entry := range m.observedSubagents {
		subagents[id] = entry
	}
	if info.Detailed != nil {
		for _, sub := range info.Detailed.Subagents {
			m.trackSubagent(sub)
			subagents[sub.ID] = subagentUI{
				status:    sub.Status,
				turnsUsed: sub.TurnsUsed,
			}
		}
	}

	ids := make([]string, 0, len(subagents))
	for id := range subagents {
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		items = append(items, modelPickerItem{
			id:      id,
			display: renderSubagentLabel(id, subagents[id]),
		})
	}
	return items
}

func (m *model) visibleMessages() []chatMessage {
	if m.transcriptView == nil {
		return m.messages
	}
	if m.transcriptView.root {
		return m.messages
	}
	return m.transcriptView.messages
}

func (m *model) enterTranscriptView(sessionID, title string) tea.Cmd {
	root := sessionID == m.sessionID || title == "main session (live)"
	m.transcriptView = &transcriptViewState{
		sessionID: sessionID,
		title:     title,
		root:      root,
	}
	m.scrollMode = true
	m.focusedToolIdx = -1
	m.input.Blur()
	m.refreshTranscriptViewState()
	m.refreshViewport()
	m.viewport.GotoBottom()
	if root {
		return nil
	}
	return scheduleTranscriptRefresh()
}

func (m *model) exitTranscriptView() {
	m.transcriptView = nil
	m.scrollMode = false
	m.focusedToolIdx = -1
	m.input.Focus()
	m.refreshViewport()
}

func (m *model) refreshTranscriptViewState() {
	if m.transcriptView == nil {
		return
	}
	if m.transcriptView.root {
		m.transcriptView.readErr = ""
		return
	}
	msgs, err := loadTranscriptMessages(m.stateDir, m.transcriptView.sessionID)
	if err != nil {
		if os.IsNotExist(err) {
			m.transcriptView.readErr = fmt.Sprintf("Waiting for transcript from %s...", shortSessionID(m.transcriptView.sessionID))
			return
		}
		m.transcriptView.readErr = fmt.Sprintf("Transcript read error: %v", err)
		return
	}
	m.transcriptView.messages = msgs
	m.transcriptView.readErr = ""
}
