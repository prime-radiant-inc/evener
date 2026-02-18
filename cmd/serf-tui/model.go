package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type model struct {
	addr      string
	connected bool
	err       error
	width     int
	height    int

	// Session info (from SSE events)
	sessionModel   string
	sessionProfile string
	sessionID      string
	turns          int

	// UI components
	viewport viewport.Model
	input    textarea.Model
	messages []chatMessage

	// Track active tool calls by call ID -> index in messages
	activeTools map[string]int
}

func newModel(addr string) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = inputPromptStyle.Render("> ")
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.Focus()
	ta.CharLimit = 0

	return model{
		addr:        addr,
		input:       ta,
		activeTools: make(map[string]int),
	}
}

func (m model) Init() tea.Cmd {
	return m.input.Focus()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Sequence(sendInterrupt(m.addr), tea.Quit)
		case "tab":
			// Toggle the most recent tool call's expanded state
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].Kind == msgTool && m.messages[i].Tool != nil && m.messages[i].Tool.Done {
					m.messages[i].Tool.Expanded = !m.messages[i].Tool.Expanded
					m.refreshViewport()
					break
				}
			}
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text != "" {
				m.messages = append(m.messages, chatMessage{Kind: msgUser, Text: text})
				m.input.Reset()
				m.refreshViewport()
				cmds = append(cmds, sendInput(m.addr, text))
			}
			return m, tea.Batch(cmds...)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		initMarkdownRenderer(m.width)
		headerHeight := 1
		inputHeight := 3
		vpHeight := m.height - headerHeight - inputHeight
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.viewport = viewport.New(m.width, vpHeight)
		m.viewport.YPosition = headerHeight
		m.input.SetWidth(m.width - 4)
		m.refreshViewport()
		return m, nil

	case sseConnectedMsg:
		m.connected = true
		return m, nil

	case sseErrorMsg:
		m.connected = false
		m.err = msg.err
		return m, nil

	case sseEventMsg:
		m.handleSSEEvent(SSEEvent(msg))
		m.refreshViewport()
		return m, nil

	case inputSentMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Error: %s", msg.err),
			})
			m.refreshViewport()
		}
		return m, nil
	}

	// Update sub-components
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *model) handleSSEEvent(ev SSEEvent) {
	switch ev.Event {
	case "SESSION_START":
		var d struct {
			SessionID string `json:"session_id"`
			Model     string `json:"model"`
			Profile   string `json:"profile"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		m.sessionID = d.SessionID
		m.sessionModel = d.Model
		m.sessionProfile = d.Profile

	case "COMMUNICATE":
		var d struct {
			Action  string `json:"action"`
			Message string `json:"message"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if d.Message != "" {
			m.messages = append(m.messages, chatMessage{Kind: msgCommunicate, Text: d.Message})
		}

	case "ASSISTANT_TEXT_END":
		m.turns++

	case "ASSISTANT_TEXT_DELTA":
		var d struct {
			Delta string `json:"delta"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		// Append to current assistant message or create new one
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Kind == msgAssistant {
			m.messages[len(m.messages)-1].Text += d.Delta
		} else {
			m.messages = append(m.messages, chatMessage{Kind: msgAssistant, Text: d.Delta})
		}

	case "TOOL_CALL_START":
		var d struct {
			CallID        string `json:"call_id"`
			ToolName      string `json:"tool_name"`
			ArgumentsJSON string `json:"arguments_json"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		desc := d.ArgumentsJSON
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		idx := len(m.messages)
		m.messages = append(m.messages, chatMessage{
			Kind: msgTool,
			Tool: &toolCallInfo{
				Name:        d.ToolName,
				Description: desc,
				Hidden:      d.ToolName == "communicate",
			},
		})
		m.activeTools[d.CallID] = idx

	case "TOOL_CALL_OUTPUT_DELTA":
		var d struct {
			CallID string `json:"call_id"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if idx, ok := m.activeTools[d.CallID]; ok && idx < len(m.messages) {
			if m.messages[idx].Tool != nil {
				m.messages[idx].Tool.Output += d.Delta
			}
		}

	case "TOOL_CALL_END":
		var d struct {
			CallID     string `json:"call_id"`
			ToolName   string `json:"tool_name"`
			Error      string `json:"error"`
			Result     string `json:"result"`
			DurationMS int64  `json:"duration_ms"`
		}
		json.Unmarshal([]byte(ev.Data), &d)
		if idx, ok := m.activeTools[d.CallID]; ok && idx < len(m.messages) {
			tc := m.messages[idx].Tool
			if tc != nil {
				tc.Done = true
				tc.Duration = time.Duration(d.DurationMS) * time.Millisecond
				tc.Error = d.Error
				if tc.Output == "" {
					tc.Output = d.Result
				}
				// Smart collapse: expand if short
				lines := strings.Count(tc.Output, "\n") + 1
				tc.Expanded = lines <= toolCollapseThreshold
			}
		}
		delete(m.activeTools, d.CallID)
	}
}

func (m *model) refreshViewport() {
	var lines []string
	for _, msg := range m.messages {
		rendered := renderMessage(msg, m.width)
		if rendered != "" {
			lines = append(lines, rendered, "") // blank line between messages
		}
	}
	content := strings.Join(lines, "\n")
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	statusBar := renderStatusBar(m.connected, m.sessionModel, m.sessionID, m.turns, m.width)
	inputView := inputBorderStyle.Width(m.width).Render(m.input.View())

	return lipgloss.JoinVertical(lipgloss.Left,
		statusBar,
		m.viewport.View(),
		inputView,
	)
}
