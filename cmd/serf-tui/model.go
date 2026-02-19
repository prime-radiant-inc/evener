package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/server"
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
	activeTools   map[string]int
	lastInterrupt time.Time
	picker        *modelPicker // non-nil when model picker is active
	themePicker   *themePicker // non-nil when theme picker is active
	scrollMode    bool         // true when scrolling history; input is blurred
}

// applyInputTheme sets the textarea's style to match the active theme colours.
// Must be called after initTheme() and again whenever the theme changes.
func applyInputTheme(ta *textarea.Model) {
	base := lipgloss.NewStyle().
		Background(activeTheme.inputBg).
		Foreground(activeTheme.inputFg)
	textStyle := lipgloss.NewStyle().Foreground(activeTheme.inputFg)
	for _, s := range []*textarea.Style{&ta.FocusedStyle, &ta.BlurredStyle} {
		s.Base = base
		s.Text = textStyle
		s.CursorLine = base
	}
	// The cursor block uses Reverse(true) which swaps fg/bg. Set the cursor
	// Style foreground to inputFg so the reversed block is visible on the
	// themed background.
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(activeTheme.inputFg)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(activeTheme.inputFg)
}

func newModel(addr string, initialMessages []chatMessage) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = inputPromptStyle.Render("> ")
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.MaxHeight = 5
	ta.Focus()
	ta.CharLimit = 0

	return model{
		addr:        addr,
		input:       ta,
		messages:    initialMessages,
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
		if m.picker != nil {
			updated, cmd := m.picker.Update(msg)
			p := updated.(modelPicker)
			m.picker = &p
			if p.done {
				m.picker = nil
				if p.selected != "" && p.selected != m.sessionModel {
					m.messages = append(m.messages, chatMessage{
						Kind: msgSystem,
						Text: fmt.Sprintf("Switching to model %s...", p.selected),
					})
					m.refreshViewport()
					return m, sendModel(m.addr, p.selected)
				}
				m.refreshViewport()
			}
			return m, cmd
		}

		if m.themePicker != nil {
			p, cmd := m.themePicker.Update(msg)
			m.themePicker = &p
			if p.done {
				m.themePicker = nil
				if p.selected != "" {
				setTheme(p.selected)
				initMarkdownRenderer(m.width)
				m.viewport.Style = viewportStyle
				applyInputTheme(&m.input)
					m.messages = append(m.messages, chatMessage{
						Kind: msgSystem,
						Text: fmt.Sprintf("Switched to %s theme.", p.selected),
					})
				}
				m.refreshViewport()
			}
			return m, cmd
		}

		// In scroll mode, route keys to the viewport; esc/i/q return to input.
		if m.scrollMode {
			switch msg.String() {
			case "esc", "i", "q":
				m.scrollMode = false
				m.input.Focus()
			default:
				m.viewport, _ = m.viewport.Update(msg)
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			now := time.Now()
			if now.Sub(m.lastInterrupt) < time.Second {
				return m, tea.Quit
			}
			m.lastInterrupt = now
			m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Interrupted. Press ctrl+c again to quit, or use /quit."})
			m.refreshViewport()
			return m, sendInterrupt(m.addr)
		case "pgup":
			m.scrollMode = true
			m.input.Blur()
			m.viewport, _ = m.viewport.Update(msg)
			return m, nil
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
		case "ctrl+s":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, tea.Batch(cmds...)
			}
			// Check for slash commands.
			if cmd, args := parseSlashCommand(text); cmd != "" {
				switch cmd {
				case "quit":
					return m, tea.Quit
				case "help":
					m.input.Reset()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: slashCommandHelp()})
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				case "status":
					m.input.Reset()
					return m, fetchStatus(m.addr)
				case "model":
					m.input.Reset()
					if args == "" {
						m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Fetching available models..."})
						m.refreshViewport()
						cmds = append(cmds, fetchModels(m.addr))
						return m, tea.Batch(cmds...)
					}
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: fmt.Sprintf("Switching to model %s...", args)})
					m.refreshViewport()
					cmds = append(cmds, sendModel(m.addr, args))
					return m, tea.Batch(cmds...)
				case "theme":
					m.input.Reset()
					p := newThemePicker()
					m.themePicker = &p
					return m, tea.Batch(cmds...)
				case "clear":
					m.input.Reset()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Starting new session..."})
					m.refreshViewport()
					cmds = append(cmds, sendClear(m.addr))
					return m, tea.Batch(cmds...)
				case "compact":
					m.input.Reset()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Compacting context..."})
					m.refreshViewport()
					cmds = append(cmds, sendCompact(m.addr))
					return m, tea.Batch(cmds...)
				default:
					m.input.Reset()
					m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: fmt.Sprintf("Unknown command: /%s", cmd)})
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				}
			}
			m.messages = append(m.messages, chatMessage{Kind: msgUser, Text: text})
			m.input.Reset()
			m.refreshViewport()
			cmds = append(cmds, sendInput(m.addr, text))
			return m, tea.Batch(cmds...)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		initMarkdownRenderer(m.width)
		m.viewport = viewport.New(m.width, m.vpHeight())
		m.viewport.YPosition = 0
		m.viewport.Style = viewportStyle
		m.input.SetWidth(m.width - 2) // -2 for the prompt "> "
		// Paint textarea rows with the theme background and foreground.
		applyInputTheme(&m.input)
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

	case statusResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Status error: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: renderDetailedStatus(msg.info, m.width),
			})
		}
		m.refreshViewport()
		return m, nil

	case clearDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Clear failed: %s", msg.err),
			})
		} else {
			m.messages = nil
			m.activeTools = make(map[string]int)
			m.turns = 0
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "New session started.",
			})
		}
		m.refreshViewport()
		return m, nil

	case modelDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Model switch failed: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Model updated.",
			})
		}
		m.refreshViewport()
		return m, nil

	case compactDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Compact failed: %s", msg.err),
			})
		} else {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "Context compacted.",
			})
		}
		m.refreshViewport()
		return m, nil

	case modelsResult:
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Could not fetch models: %s\nUse /model <name> to switch manually.", msg.err),
			})
			m.refreshViewport()
			return m, nil
		}
		if len(msg.models) == 0 {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: "No models available from provider.",
			})
			m.refreshViewport()
			return m, nil
		}
		picker := newModelPicker(msg.models, m.sessionModel, m.width)
		m.picker = &picker
		// Remove the "Fetching..." message.
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Text == "Fetching available models..." {
			m.messages = m.messages[:len(m.messages)-1]
		}
		m.refreshViewport()
		return m, nil
	}

	// Update sub-components — only pass non-key messages to the viewport so its
	// built-in key bindings don't fire while the user is typing. Key events go
	// only to the input; the viewport is updated via scroll mode above.
	var cmd tea.Cmd
	if _, isKey := msg.(tea.KeyMsg); isKey {
		prevHeight := m.input.Height()
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		// If the textarea grew or shrank, update the viewport height.
		if m.input.Height() != prevHeight {
			m.viewport.Height = m.vpHeight()
		}
	} else {
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

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

// vpHeight returns the height the viewport should occupy given current terminal
// and input dimensions. statusBar=1, border=1, textarea rows=input.Height().
func (m model) vpHeight() int {
	h := m.height - 1 - 1 - m.input.Height()
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) refreshViewport() {
	// Resize viewport if input height changed (textarea grew or shrank).
	if newH := m.vpHeight(); newH != m.viewport.Height {
		m.viewport.Height = newH
	}
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

	bgStyle := lipgloss.NewStyle().
		Background(activeTheme.viewportBg).
		Width(m.width).
		Height(m.height)

	statusBar := renderStatusBar(m.connected, m.sessionModel, m.sessionID, m.turns, m.scrollMode, m.width)

	var body string
	if m.picker != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			m.picker.View(),
		)
	} else if m.themePicker != nil {
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			m.themePicker.View(),
		)
	} else {
		inputView := inputBorderStyle.Width(m.width).Render(m.input.View())
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			statusBar,
			inputView,
		)
	}

	return bgStyle.Render(body)
}

// renderDetailedStatus formats a StatusInfo into a multi-panel text display.
// All sections are shown (with counts) even when empty.
// Tool lists wrap at width to avoid horizontal scrolling.
func renderDetailedStatus(info server.StatusInfo, width int) string {
	if width <= 0 {
		width = 80
	}

	var b strings.Builder

	// Header section (always shown).
	pressure := fmt.Sprintf("%.0f%%", info.ContextPressure*100)
	b.WriteString(fmt.Sprintf("Session:  %s\nModel:    %s (%s)\nTurns:    %d\nContext:  %s used",
		info.SessionID, info.Model, info.Profile, info.Turns, pressure))

	ds := info.Detailed
	if ds == nil {
		return b.String()
	}

	// Tools section — group by source.
	core := []string{}
	mcp := map[string][]string{} // server name → tool names
	custom := []string{}

	for _, t := range ds.Tools {
		switch {
		case t.Source == "core":
			core = append(core, t.Name)
		case strings.HasPrefix(t.Source, "mcp:"):
			srv := t.Source[4:]
			mcp[srv] = append(mcp[srv], t.Name)
		default:
			custom = append(custom, t.Name)
		}
	}

	b.WriteString(fmt.Sprintf("\n\nTools (%d):", len(ds.Tools)))
	if len(core) > 0 {
		writeWrappedList(&b, "Core:", core, width)
	}
	for srv, tools := range mcp {
		writeWrappedList(&b, fmt.Sprintf("MCP [%s]:", srv), tools, width)
	}
	if len(custom) > 0 {
		writeWrappedList(&b, "Custom:", custom, width)
	}

	// MCP Servers section.
	b.WriteString(fmt.Sprintf("\n\nMCP Servers (%d):", len(ds.MCP)))
	for _, srv := range ds.MCP {
		b.WriteString(fmt.Sprintf("\n  %s (%d tools)", srv.Name, len(srv.Tools)))
	}

	// Skills section.
	b.WriteString(fmt.Sprintf("\n\nSkills (%d):", len(ds.Skills)))
	for _, skill := range ds.Skills {
		b.WriteString(fmt.Sprintf("\n  %s", skill.Name))
	}

	// Plugins section.
	b.WriteString(fmt.Sprintf("\n\nPlugins (%d):", len(ds.Plugins)))
	for _, p := range ds.Plugins {
		version := p.Version
		if version == "" {
			version = "?"
		}
		b.WriteString(fmt.Sprintf("\n  %s v%s (%d skills, %d agents, %d hooks)",
			p.Name, version, p.SkillCount, p.AgentCount, p.HookCount))
	}

	// Hooks section.
	b.WriteString(fmt.Sprintf("\n\nHooks (%d):", len(ds.Hooks)))
	if len(ds.Hooks) > 0 {
		parts := []string{}
		for event, count := range ds.Hooks {
			parts = append(parts, fmt.Sprintf("%s: %d", event, count))
		}
		sort.Strings(parts)
		b.WriteString("\n  " + strings.Join(parts, "  "))
	}

	// Subagents section.
	b.WriteString(fmt.Sprintf("\n\nSubagents (%d):", len(ds.Subagents)))
	for _, sub := range ds.Subagents {
		b.WriteString(fmt.Sprintf("\n  %s (%s, %d turns)", sub.ID, sub.Status, sub.TurnsUsed))
	}

	// Plugin agents section.
	b.WriteString(fmt.Sprintf("\n\nAgents (%d):", len(ds.Agents)))
	for _, name := range ds.Agents {
		b.WriteString(fmt.Sprintf("\n  %s", name))
	}

	return b.String()
}

// writeWrappedList writes a labeled comma-separated list that wraps at width.
// Output format: "\n  Label: item1, item2,\n         item3, item4"
func writeWrappedList(b *strings.Builder, label string, items []string, width int) {
	prefix := "  " + label + " "
	indent := strings.Repeat(" ", len(prefix))

	b.WriteString("\n" + prefix)
	col := len(prefix)
	for i, item := range items {
		entry := item
		if i < len(items)-1 {
			entry += ","
		}
		needed := len(entry)
		if i > 0 {
			needed++ // space before item
		}
		if col+needed > width && col > len(prefix) {
			b.WriteString("\n" + indent)
			col = len(indent)
		} else if i > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(entry)
		col += len(entry)
	}
}
