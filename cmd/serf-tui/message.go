package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
)

var markdownRenderer *glamour.TermRenderer
var markdownRendererWidth int

func initMarkdownRenderer(width int) {
	if width <= 0 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(markdownStyleName()),
		glamour.WithWordWrap(max(1, width-4)),
	)
	if err == nil {
		markdownRenderer = r
		markdownRendererWidth = width
	}
}

func markdownStyleName() string {
	if effectiveTUITheme() == lightTheme {
		return "light"
	}
	return "dark"
}

func renderMarkdown(text string, width int) string {
	if !containsMarkdownSyntax(text) {
		return text
	}
	if markdownRenderer == nil || markdownRendererWidth != width {
		initMarkdownRenderer(width)
	}
	if markdownRenderer == nil {
		return text
	}
	rendered, err := markdownRenderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

func containsMarkdownSyntax(text string) bool {
	if strings.ContainsAny(text, "`*_[]") {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") ||
			strings.HasPrefix(line, "## ") ||
			strings.HasPrefix(line, "### ") ||
			strings.HasPrefix(line, "> ") ||
			strings.HasPrefix(line, "- ") ||
			strings.HasPrefix(line, "+ ") {
			return true
		}
		if isOrderedMarkdownListItem(line) {
			return true
		}
	}
	return false
}

func isOrderedMarkdownListItem(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

type messageKind int

const (
	msgUser        messageKind = iota
	msgAssistant               // LLM thinking/reasoning text
	msgCommunicate             // agent's communicate output (the actual response)
	msgTool
	msgSystem
)

type toolCallInfo struct {
	Name        string
	Description string // compact one-liner header
	Detail      string // rich multi-line body shown when expanded
	Output      string
	Error       string
	Duration    time.Duration
	Expanded    bool
	Done        bool
	Hidden      bool // suppress from display (e.g. communicate)
}

const toolCollapseThreshold = 5

func renderMessage(msg chatMessage, width int, focused bool) string {
	messageWidth := width
	if focused {
		messageWidth = max(1, width-2)
	}
	switch msg.Kind {
	case msgUser:
		return renderSelectedMessage(userBlockStyle.Width(messageWidth).Render("> "+msg.Text), focused)
	case msgAssistant:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ""
		}
		return renderSelectedMessage(thinkingStyle.Width(messageWidth).Render(renderMarkdown(text, messageWidth)), focused)
	case msgCommunicate:
		return renderSelectedMessage(communicateStyle.Width(messageWidth).Render(renderMarkdown(msg.Text, messageWidth)), focused)
	case msgTool:
		if msg.Tool == nil || msg.Tool.Hidden {
			return ""
		}
		return renderToolCall(*msg.Tool, width, focused)
	case msgSystem:
		return renderSelectedMessage(systemStyle.Width(messageWidth).Render(msg.Text), focused)
	}
	return ""
}

func renderSelectedMessage(rendered string, focused bool) string {
	if !focused || rendered == "" {
		return rendered
	}
	lines := strings.Split(rendered, "\n")
	lines[0] = "▶ " + lines[0]
	return strings.Join(lines, "\n")
}

func renderToolCall(tc toolCallInfo, width int, focused bool) string {
	// Show a distinct arrow when focused.
	arrow := "▸"
	if tc.Expanded {
		arrow = "▾"
	}
	if focused {
		arrow = "▶"
	}

	dur := ""
	if tc.Done {
		dur = toolDurationStyle.Render(fmt.Sprintf("[%.1fs]", tc.Duration.Seconds()))
	} else {
		dur = toolDurationStyle.Render("...")
	}

	name := toolNameStyle.Render(tc.Name)

	// Prefix width: "arrow SP name  " (2 spaces after name before desc)
	prefixWidth := lipgloss.Width(arrow) + 1 + lipgloss.Width(name) + 2
	durWidth := lipgloss.Width(dur)
	indent := strings.Repeat(" ", prefixWidth)

	// Wrap the description across lines.
	// First line budget: width - prefixWidth - 2 (gap before dur) - durWidth
	// Continuation line budget: width - prefixWidth
	// dur is appended after a 2-space gap on the last line.
	firstBudget := width - prefixWidth - 2 - durWidth
	contBudget := width - prefixWidth
	if firstBudget < 1 {
		firstBudget = 1
	}
	if contBudget < 1 {
		contBudget = 1
	}

	descLines := wrapText(tc.Description, firstBudget, contBudget)

	var headerLines []string
	for i, dl := range descLines {
		var line string
		if i == 0 {
			line = fmt.Sprintf("%s %s  %s", arrow, name, dl)
		} else {
			line = indent + dl
		}
		// Append dur after the last description line.
		if i == len(descLines)-1 {
			line = line + "  " + dur
		}
		headerLines = append(headerLines, line)
	}
	// Edge case: empty description — just show arrow name dur.
	if len(descLines) == 0 {
		headerLines = []string{fmt.Sprintf("%s %s  %s", arrow, name, dur)}
	}

	header := strings.Join(headerLines, "\n")

	if !tc.Expanded || (tc.Detail == "" && tc.Output == "" && tc.Error == "") {
		return toolCollapsedStyle.Render(header)
	}

	var body strings.Builder
	if tc.Detail != "" {
		body.WriteString(toolExpandedStyle.Width(width - 4).Render(tc.Detail))
	}
	if tc.Output != "" {
		if body.Len() > 0 {
			body.WriteString("\n")
		}
		body.WriteString(toolExpandedStyle.Width(width - 4).Render(tc.Output))
	}
	if tc.Error != "" {
		if body.Len() > 0 {
			body.WriteString("\n")
		}
		body.WriteString(toolExpandedStyle.Width(width - 4).Render("error: " + tc.Error))
	}
	return header + "\n" + body.String()
}

// wrapText splits text into lines. The first line is at most firstBudget runes
// wide; subsequent lines are at most contBudget runes wide. Splits on
// whitespace boundaries when possible, otherwise hard-breaks.
func wrapText(text string, firstBudget, contBudget int) []string {
	if text == "" {
		return nil
	}
	var lines []string
	budget := firstBudget
	for len(text) > 0 {
		if len(text) <= budget {
			lines = append(lines, text)
			break
		}
		// If the character at budget is a space, split cleanly there.
		split := budget
		if text[budget] != ' ' {
			// Find the last space within budget to avoid splitting a word.
			if idx := strings.LastIndex(text[:budget], " "); idx > 0 {
				split = idx
			}
		}
		lines = append(lines, strings.TrimRight(text[:split], " "))
		text = strings.TrimLeft(text[split:], " ")
		budget = contBudget
	}
	return lines
}

type chatMessage struct {
	Kind       messageKind
	Text       string
	TurnIndex  int
	ItemID     string
	ToolCallID string
	Tool       *toolCallInfo
}

// historyToMessages converts session history turns into TUI chat messages
// for display when resuming a session.
func historyToMessages(turns []agent.Turn) []chatMessage {
	// Collect tool results keyed by call ID for matching with tool calls.
	toolResults := make(map[string]llm.ToolResultData)
	for _, t := range turns {
		if t.Kind != agent.TurnToolResults && t.Kind != agent.TurnTool {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				toolResults[p.ToolResult.ToolCallID] = *p.ToolResult
			}
		}
	}

	var msgs []chatMessage
	for _, t := range turns {
		switch t.Kind {
		case agent.TurnUserInput:
			text := t.Message.Text()
			if strings.TrimSpace(text) != "" {
				msgs = append(msgs, chatMessage{Kind: msgUser, Text: text})
			}

		case agent.TurnAssistant:
			for _, p := range t.Message.Content {
				switch p.Kind {
				case llm.ContentText:
					// Skip empty text (common in tool-only responses).
					if strings.TrimSpace(p.Text) != "" {
						msgs = append(msgs, chatMessage{Kind: msgAssistant, Text: p.Text})
					}

				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					tc := p.ToolCall
					if tc.Name == "communicate" {
						msg := extractCommunicate(tc)
						if msg != "" {
							msgs = append(msgs, chatMessage{Kind: msgCommunicate, Text: msg})
						}
						continue
					}

					// Non-communicate tool call: show as collapsed tool entry.
					argsJSON := string(tc.Arguments)
					toolDesc, toolDetail := summarizeTool(tc.Name, argsJSON)
					result := toolResults[tc.ID]
					output := fmt.Sprintf("%v", result.Content)
					info := &toolCallInfo{
						Name:        tc.Name,
						Description: toolDesc,
						Detail:      toolDetail,
						Output:      output,
						Done:        true,
						Expanded:    toolDetail != "",
					}
					if result.IsError {
						info.Error = output
					}
					msgs = append(msgs, chatMessage{Kind: msgTool, Tool: info})
				}
			}
		}
	}
	return msgs
}

// extractCommunicate pulls the message field from a communicate tool call.
func extractCommunicate(tc *llm.ToolCallData) string {
	var args struct {
		Message string `json:"message"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if err := json.Unmarshal(tc.Arguments, &args); err == nil {
		if args.Message != "" {
			return args.Message
		}
		if args.Output != nil && args.Output.Message != "" {
			return args.Output.Message
		}
	}
	return ""
}
