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

func initMarkdownRenderer(width int) {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-4),
	)
	if err == nil {
		markdownRenderer = r
	}
}

func renderMarkdown(text string) string {
	if markdownRenderer == nil {
		return text
	}
	rendered, err := markdownRenderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

type messageKind int

const (
	msgUser messageKind = iota
	msgAssistant    // LLM thinking/reasoning text
	msgCommunicate  // agent's communicate output (the actual response)
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

func renderMessage(msg chatMessage, width int) string {
	switch msg.Kind {
	case msgUser:
		return userBlockStyle.Width(width).Render("> " + msg.Text)
	case msgAssistant:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ""
		}
		return thinkingStyle.Render(text)
	case msgCommunicate:
		return communicateStyle.Render(renderMarkdown(msg.Text))
	case msgTool:
		if msg.Tool == nil || msg.Tool.Hidden {
			return ""
		}
		return renderToolCall(*msg.Tool, width)
	case msgSystem:
		return systemStyle.Render(msg.Text)
	}
	return ""
}

func renderToolCall(tc toolCallInfo, width int) string {
	arrow := "▸"
	if tc.Expanded {
		arrow = "▾"
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

	if !tc.Expanded || (tc.Detail == "" && tc.Output == "") {
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
	Kind messageKind
	Text string
	Tool *toolCallInfo
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
						msg := extractCommunicateMessage(tc)
						if msg != "" {
							msgs = append(msgs, chatMessage{Kind: msgCommunicate, Text: msg})
						}
						continue
					}

				// Non-communicate tool call: show as collapsed tool entry.
				desc := string(tc.Arguments)
					result := toolResults[tc.ID]
					output := fmt.Sprintf("%v", result.Content)
					info := &toolCallInfo{
						Name:        tc.Name,
						Description: desc,
						Output:      output,
						Done:        true,
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

// extractCommunicateMessage pulls the message field from a communicate tool call's arguments.
func extractCommunicateMessage(tc *llm.ToolCallData) string {
	var args struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(tc.Arguments, &args); err == nil && args.Message != "" {
		return args.Message
	}
	return ""
}
