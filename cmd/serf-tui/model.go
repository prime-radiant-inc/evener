package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/server"
)

type model struct {
	addr      string
	stateDir  string
	connected bool
	err       error
	width     int
	height    int

	// Session info (from appwire events)
	sessionModel      string
	sessionProfile    string
	sessionID         string
	turns             int
	contextTokens     int // input tokens from last ASSISTANT_TEXT_END
	contextWindowSize int // context window size from SESSION_START
	processing        bool
	turnInputTokens   int // input tokens accumulated since last USER_INPUT
	turnOutputTokens  int // output tokens accumulated since last USER_INPUT

	// UI components
	viewport viewport.Model
	input    textarea.Model
	messages []chatMessage

	// Input history
	history      []string // escaped entries (one per line)
	historyIdx   int      // -1 = not browsing; len(history) = back to draft
	historyDraft string   // saved current input when entering history browse

	// Track active transcript items by item/call ID -> index in messages.
	activeTools       map[string]int
	activeMessages    map[string]int
	picker           *modelPicker // non-nil when model picker is active
	transcriptPicker *modelPicker // non-nil when transcript picker is active
	themePicker      *themePicker // non-nil when theme picker is active
	scrollMode       bool         // true when scrolling history; input is blurred
	focusedToolIdx   int          // index into messages of focused tool call in scroll mode; -1 = none
}

// applyInputTheme sets the textarea's style to match the active theme colours.
// Must be called after initTheme() and again whenever the theme changes.
func applyInputTheme(ta *textarea.Model) {
	th := activeTheme()
	base := lipgloss.NewStyle().
		Background(th.BgRaised).
		Foreground(th.Text)
	textStyle := lipgloss.NewStyle().Foreground(th.Text)
	for _, s := range []*textarea.Style{&ta.FocusedStyle, &ta.BlurredStyle} {
		s.Base = base
		s.Text = textStyle
		s.CursorLine = base
	}
	// The cursor block uses Reverse(true) which swaps fg/bg. Set the cursor
	// Style foreground to Text so the reversed block is visible on the
	// themed background.
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(th.Text)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(th.Text)
}

func newModel(addr, stateDir string, initialMessages []chatMessage) model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.MaxHeight = 5
	ta.Focus()
	ta.CharLimit = 0
	// Disable the textarea's own newline binding — we handle enter ourselves.
	ta.KeyMap.InsertNewline.SetEnabled(false)
	applyInputTheme(&ta)

	return model{
		addr:              addr,
		stateDir:          stateDir,
		input:             ta,
		messages:          initialMessages,
		activeTools:    make(map[string]int),
		activeMessages: make(map[string]int),
		history:        loadHistory(stateDir),
		historyIdx:     -1,
	}
}

// toolIndices returns the indices in m.messages that are msgTool entries.
func (m *model) toolIndices() []int {
	var idx []int
	for i := range m.messages {
		if m.messages[i].Kind == msgTool && m.messages[i].Tool != nil && !m.messages[i].Tool.Hidden {
			idx = append(idx, i)
		}
	}
	return idx
}

// focusTool moves the focused tool to the previous (dir=-1) or next (dir=+1) tool.
func (m *model) focusTool(dir int) {
	indices := m.toolIndices()
	if len(indices) == 0 {
		return
	}
	if m.focusedToolIdx < 0 {
		// No focus yet; default to the last tool.
		m.focusedToolIdx = indices[len(indices)-1]
		return
	}
	// Find current position in the list.
	curPos := -1
	for i, idx := range indices {
		if idx == m.focusedToolIdx {
			curPos = i
			break
		}
	}
	if curPos < 0 {
		// Focused index is not in the list (e.g., hidden); reset to last.
		m.focusedToolIdx = indices[len(indices)-1]
		return
	}
	curPos += dir
	if curPos < 0 {
		curPos = 0
	} else if curPos >= len(indices) {
		curPos = len(indices) - 1
	}
	m.focusedToolIdx = indices[curPos]
}

// isToolFocused returns true if the given message index is the focused tool.
func (m *model) isToolFocused(msgIdx int) bool {
	return m.scrollMode && msgIdx == m.focusedToolIdx
}

// scrollToMessage scrolls the viewport so the rendered output for the given
// message index is visible, roughly centered in the viewport.
func (m *model) scrollToMessage(msgIdx int) {
	line := 0
	for i, msg := range m.messages {
		focused := m.isToolFocused(i)
		rendered := renderMessage(msg, m.width, focused)
		if rendered == "" {
			continue
		}
		if i == msgIdx {
			// Center the target in the viewport.
			target := line - m.viewport.Height/2
			if target < 0 {
				target = 0
			}
			m.viewport.SetYOffset(target)
			return
		}
		line += strings.Count(rendered, "\n") + 2 // +1 for the line itself, +1 for blank separator
	}
}

// resetInput clears the input and shrinks it back to one line.
func (m *model) resetInput() {
	m.input.Reset()
	m.input.Placeholder = "Type a message..."
	m.input.SetHeight(1)
	m.viewport.Height = m.vpHeight()
	m.historyIdx = -1
	m.historyDraft = ""
}

// setInputValue replaces the textarea contents and adjusts its height.
func (m *model) setInputValue(s string) {
	m.input.Reset()
	m.input.SetValue(s)
	wantH := m.input.LineCount()
	if wantH < 1 {
		wantH = 1
	}
	if wantH > m.input.MaxHeight {
		wantH = m.input.MaxHeight
	}
	m.input.SetHeight(wantH)
	m.viewport.Height = m.vpHeight()
}

// addHistory appends text to the in-memory history and persists it to disk.
func (m *model) addHistory(text string) {
	escaped := strings.ReplaceAll(text, "\n", `\n`)
	m.history = append(m.history, escaped)
	if len(m.history) > maxHistoryEntries {
		m.history = m.history[len(m.history)-maxHistoryEntries:]
	}
	appendHistory(m.stateDir, text)
}

// vpHeight computes the viewport height from window and input dimensions.
// statusBar=1, border=1, textarea rows=input.Height().
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
	currentMessages := m.messages
	for i, msg := range currentMessages {
		focused := m.isToolFocused(i)
		rendered := renderMessage(msg, m.width, focused)
		if rendered != "" {
			lines = append(lines, rendered, "") // blank line between messages
		}
	}
	content := strings.Join(lines, "\n")
	m.viewport.SetContent(content)
	if !m.scrollMode {
		m.viewport.GotoBottom()
	}
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

// renderTasks formats a list of agent tasks for display.
func renderTasks(tasks []agent.Task, width int) string {
	if width <= 0 {
		width = 80
	}
	if len(tasks) == 0 {
		return "No tasks."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tasks (%d):\n", len(tasks)))

	statusIcon := map[agent.TaskStatus]string{
		agent.TaskOpen:       "○",
		agent.TaskInProgress: "◐",
		agent.TaskDone:       "●",
		agent.TaskCancelled:  "✗",
	}

	for _, t := range tasks {
		icon := statusIcon[t.Status]
		if icon == "" {
			icon = "?"
		}
		b.WriteString(fmt.Sprintf("  %s [%d] %s — %s", icon, t.ID, t.Type, t.Description))
		if len(t.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf(" (depends on: %v)", t.DependsOn))
		}
		if t.ReasoningEffort != "" {
			b.WriteString(fmt.Sprintf(" [%s]", t.ReasoningEffort))
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}
