package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmd/serf-tui/internal/toolsummary"
	"primeradiant.com/serf/llm"
)

var markdownRenderer *glamour.TermRenderer
var markdownRendererWidth int

func initMarkdownRenderer(width int) {
	if width <= 0 {
		width = 80
	}
	style := themedGlamourStyle()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(max(1, width-4)),
	)
	if err == nil {
		markdownRenderer = r
		markdownRendererWidth = width
	}
}

// themedGlamourStyle builds a glamour StyleConfig from the active theme,
// starting from glamour's stock light/dark config and overriding the bits
// that don't follow the surrounding theme — chiefly the code-block and
// inline-code backgrounds, which ship as fixed dark greys ("#373737") even
// in the "light" style.
func themedGlamourStyle() ansi.StyleConfig {
	th := activeTheme()
	var base ansi.StyleConfig
	if th.Name == "light" {
		base = styles.LightStyleConfig
	} else {
		base = styles.DarkStyleConfig
	}

	bgRaised := string(th.BgRaised)
	text := string(th.Text)
	textMuted := string(th.TextMuted)

	// Inline code: very subtle raised tone — just enough to register as a
	// distinct span without reading as a highlighted block.
	base.Code.BackgroundColor = strPtr(bgRaised)
	base.Code.Color = strPtr(text)

	// Code block container: deep-clone Chroma so we don't mutate the
	// package-level styles.LightStyleConfig / DarkStyleConfig.
	if base.CodeBlock.Chroma != nil {
		chromaCopy := *base.CodeBlock.Chroma
		chromaCopy.Background.BackgroundColor = strPtr(bgRaised)
		chromaCopy.Background.Color = strPtr(text)
		chromaCopy.Text.Color = strPtr(text)
		base.CodeBlock.Chroma = &chromaCopy
	}
	base.CodeBlock.BackgroundColor = strPtr(bgRaised)
	base.CodeBlock.Color = strPtr(text)

	// Block quote: muted text in the theme tone.
	base.BlockQuote.Color = strPtr(textMuted)

	return base
}

func strPtr(s string) *string { return &s }

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

// markdownRendererCached returns the current renderer cache; nil means
// the cache is empty. For testing only — exposed to verify that
// applyThemeName invalidates the renderer cache.
func markdownRendererCached() *glamour.TermRenderer {
	return markdownRenderer
}

func resetMarkdownRenderer() {
	markdownRenderer = nil
}

func init() {
	markdownInvalidator = resetMarkdownRenderer
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
	msgSteering // user-initiated steering placeholder + authoritative steering chip
)

type toolCallInfo struct {
	Name        string
	Description string // compact one-liner header
	Detail      string // rich multi-line body shown when expanded
	RawArgs     string // raw JSON arguments string; preferred over Description for arg parsing
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

	// Pending / failed prefix. Applied uniformly across all message
	// kinds so the optimistic-rendering visual is consistent.
	prefix := ""
	suffix := ""
	if msg.Pending {
		prefix = lipgloss.NewStyle().Faint(true).Render("⠋ ")
	}
	if msg.Failed {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗ ")
		if msg.Reason != "" {
			suffix = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("9")).Render(" (failed: " + msg.Reason + ")")
		}
	}
	body := prefix + msg.Text + suffix

	switch msg.Kind {
	case msgUser:
		th := activeTheme()
		barClr := th.Accent
		bar := lipgloss.NewStyle().Foreground(barClr).Render("┃")
		if focused {
			bar = lipgloss.NewStyle().Foreground(barClr).Render("┃┃")
		}
		rendered := userBlockStyle.Width(max(1, messageWidth-lipgloss.Width(bar)-1)).Render("> " + body)
		return bar + " " + rendered
	case msgAssistant:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ""
		}
		th := activeTheme()
		bar := StateBar(th.StateProcessing)
		barW := lipgloss.Width(bar)
		rendered := thinkingStyle.Width(max(1, messageWidth-barW-1)).Render(renderMarkdown(text, max(1, messageWidth-barW-1)))
		return bar + " " + renderSelectedMessage(rendered, focused)
	case msgCommunicate:
		return renderSelectedMessage(communicateStyle.Width(messageWidth).Render(renderMarkdown(msg.Text, messageWidth)), focused)
	case msgTool:
		if msg.Tool == nil || msg.Tool.Hidden {
			return ""
		}
		return renderToolCall(*msg.Tool, width, focused)
	case msgSystem:
		return renderSelectedMessage(systemStyle.Width(messageWidth).Render(body), focused)
	case msgSteering:
		// Steering placeholder or authoritative chip. systemStyle is the
		// closest existing style; refine later if needed.
		return renderSelectedMessage(systemStyle.Width(messageWidth).Render("↻ "+body), focused)
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
	r, _ := lookupToolRenderer(tc.Name)
	// Prefer RawArgs (populated from source ArgumentsJSON). Fall back to
	// extracting JSON from Description for legacy paths where RawArgs is empty.
	rawJSON := tc.RawArgs
	if rawJSON == "" {
		rawJSON = argsJSONFromDescription(tc.Description)
	}
	args := toolArgsFromJSON(rawJSON)

	verb := r.Verb(args)
	target := r.Target(args)
	var result string
	if tc.Done || tc.Error != "" {
		result = r.Result(args, tc.Output, tc.Error, tc.Duration)
	}

	th := activeTheme()
	stateClr := stateColorForToolDone(tc.Done, tc.Error)
	bar := StateBar(stateClr)
	check := lipgloss.NewStyle().Foreground(stateClr).Render(checkmarkFor(tc.Done, tc.Error))
	verbStyled := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render(verb)
	targetStyled := lipgloss.NewStyle().Foreground(th.Text).Render(target)

	durText := ""
	if tc.Done {
		durText = formatDur(tc.Duration)
	} else {
		durText = "…"
	}

	left := bar + " " + check + " " + verbStyled + "  " + targetStyled
	right := lipgloss.NewStyle().Foreground(th.TextDim).Render(result) + "  " +
		lipgloss.NewStyle().Foreground(th.TextGhost).Render(durText)

	header := DotLeader(left, right, width)
	if focused {
		// Replace the single state bar with the double focus bar.
		header = strings.Replace(header, bar, FocusedStateBar(th.Accent), 1)
	}

	var bodyLines []string
	if purpose := strings.TrimSpace(args.Str("purpose")); purpose != "" {
		purposeLine := lipgloss.NewStyle().Italic(true).Render(purpose)
		bodyLines = append(bodyLines, indentBlock(purposeLine, th.IndentToolBody))
	}

	// Show expanded body: renderer Body func takes priority; fall back to
	// tc.Detail / tc.Output / tc.Error for backward compatibility.
	expanded := tc.Expanded || r.ExpandedByDefault
	if !expanded {
		if len(bodyLines) == 0 {
			return header
		}
		return header + "\n" + strings.Join(bodyLines, "\n")
	}

	bodyFromRenderer := false
	if r.Body != nil {
		body := r.Body(args, tc.Output, width-th.IndentToolBody)
		if body != "" {
			// Append error after renderer body so errors from unknown/MCP tools
			// are always visible even when JSON output is also present.
			if tc.Error != "" {
				errStyle := lipgloss.NewStyle().Foreground(activeTheme().StateAwaiting)
				body = body + "\n" + errStyle.Render(tc.Error)
			}
			bodyLines = append(bodyLines, indentBlock(body, th.IndentToolBody))
			bodyFromRenderer = true
		}
	}
	if !bodyFromRenderer {
		// Legacy fallback: show Detail / Output / Error.
		// Used when there is no Body renderer, or the renderer returned empty
		// (e.g. read_file Body on an errored call with no output).
		if tc.Detail != "" {
			bodyLines = append(bodyLines, toolExpandedStyle.Width(width-4).Render(tc.Detail))
		}
		if tc.Output != "" {
			bodyLines = append(bodyLines, toolExpandedStyle.Width(width-4).Render(tc.Output))
		}
		if tc.Error != "" {
			bodyLines = append(bodyLines, toolExpandedStyle.Width(width-4).Render("error: "+tc.Error))
		}
	}

	if len(bodyLines) == 0 {
		return header
	}
	return header + "\n" + strings.Join(bodyLines, "\n")
}

func stateColorForToolDone(done bool, errStr string) lipgloss.Color {
	th := activeTheme()
	if errStr != "" {
		return th.StateAwaiting
	}
	if done {
		return th.StateIdle
	}
	return th.StateProcessing
}

func checkmarkFor(done bool, errStr string) string {
	if errStr != "" {
		return "✕"
	}
	if done {
		return "✓"
	}
	return "·"
}

func formatDur(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d/time.Millisecond)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func indentBlock(s string, indent int) string {
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// argsJSONFromDescription extracts the embedded JSON args from a
// toolCallInfo.Description if present, else returns "".
// The existing toolCallInfo.Description is a human summary or raw JSON.
// We detect JSON by checking if the string starts with '{'.
func argsJSONFromDescription(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	return ""
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
	TurnID     string
	TurnIndex  int
	ItemID     string
	ToolCallID string
	Tool       *toolCallInfo

	// PendingID is non-zero when this message is an optimistic placeholder
	// created in response to a user click before the authoritative event
	// arrives. It matches the PendingEntry.ID from the pending coordinator (pendingpkg).
	PendingID int64
	// Pending is true while the optimistic call is in flight. The renderer
	// prefixes the row with a spinner glyph and dims the color while true.
	Pending bool
	// Failed is true if the optimistic call rejected or timed out without
	// reconciling. Mutually exclusive with Pending. Renderer shows a red
	// ✗ prefix and the Reason.
	Failed bool
	// Reason is the failure message when Failed is true.
	Reason string
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
					toolDesc, toolDetail := toolsummary.SummarizeTool(tc.Name, argsJSON)
					result := toolResults[tc.ID]
					output := fmt.Sprintf("%v", result.Content)
					info := &toolCallInfo{
						Name:        tc.Name,
						Description: toolDesc,
						Detail:      toolDetail,
						RawArgs:     argsJSON,
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
