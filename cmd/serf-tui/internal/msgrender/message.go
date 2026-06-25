package msgrender

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-tui/internal/toolsummary"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
	"primeradiant.com/serf/llm"
)

var markdownRenderer *glamour.TermRenderer
var markdownRendererWidth int

func InitMarkdownRenderer(width int) {
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
	th := tuitheme.ActiveTheme()
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
		InitMarkdownRenderer(width)
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
// tuitheme.ApplyThemeName invalidates the renderer cache.
func markdownRendererCached() *glamour.TermRenderer {
	return markdownRenderer
}

func resetMarkdownRenderer() {
	markdownRenderer = nil
}

func init() {
	tuitheme.SetMarkdownInvalidator(resetMarkdownRenderer)
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

// reasoningGist distills a collapsed thought to its first content line, clipped
// to a scannable length so a stack of finished thoughts stays legible.
func reasoningGist(text string) string {
	const maxLen = 72
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if len(line) > maxLen {
			line = strings.TrimRight(line[:maxLen], " ") + "…"
		}
		return line
	}
	return ""
}

func RenderMessage(msg transcript.ChatMessage, width int, focused bool) string {
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
	case transcript.MsgUser:
		th := tuitheme.ActiveTheme()
		barClr := th.Accent
		bar := lipgloss.NewStyle().Foreground(barClr).Render("┃")
		if focused {
			bar = lipgloss.NewStyle().Foreground(barClr).Render("┃┃")
		}
		rendered := tuitheme.UserBlockStyle.Width(max(1, messageWidth-lipgloss.Width(bar)-1)).Render("> " + body)
		return bar + " " + rendered
	case transcript.MsgAssistant:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ""
		}
		th := tuitheme.ActiveTheme()
		bar := tuiprim.StateBar(th.StateProcessing)
		barW := lipgloss.Width(bar)
		rendered := tuitheme.ThinkingStyle.Width(max(1, messageWidth-barW-1)).Render(renderMarkdown(text, max(1, messageWidth-barW-1)))
		return bar + " " + RenderSelectedMessage(rendered, focused)
	case transcript.MsgReasoning:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ""
		}
		th := tuitheme.ActiveTheme()
		spark := lipgloss.NewStyle().Foreground(th.TextDim).Render("✦")
		// Collapsed once the turn moves on: a single quiet line of gist, until the
		// reader re-opens it with ctrl+t. While it is the current turn the whole
		// thought streams open, plain (not markdown) so it stays the quietest
		// entry and never reflows on heading syntax.
		if msg.Done && !msg.Expanded {
			return RenderSelectedMessage(spark+" "+tuitheme.ThinkingStyle.Render(reasoningGist(text)), focused)
		}
		bodyWidth := max(1, messageWidth-lipgloss.Width(spark)-1)
		rendered := tuitheme.ThinkingStyle.Width(bodyWidth).Render(text)
		return spark + " " + RenderSelectedMessage(rendered, focused)
	case transcript.MsgCommunicate:
		return RenderSelectedMessage(tuitheme.CommunicateStyle.Width(messageWidth).Render(renderMarkdown(msg.Text, messageWidth)), focused)
	case transcript.MsgTool:
		if msg.Tool == nil || msg.Tool.Hidden {
			return ""
		}
		return RenderToolCall(*msg.Tool, width, focused)
	case transcript.MsgSystem:
		return RenderSelectedMessage(tuitheme.SystemStyle.Width(messageWidth).Render(body), focused)
	case transcript.MsgSteering:
		// Steering placeholder or authoritative chip. tuitheme.SystemStyle is the
		// closest existing style; refine later if needed.
		return RenderSelectedMessage(tuitheme.SystemStyle.Width(messageWidth).Render("↻ "+body), focused)
	}
	return ""
}

// SelectionPrefix marks the first line of the browse-mode selected message.
const SelectionPrefix = "▶ "

func RenderSelectedMessage(rendered string, focused bool) string {
	if !focused || rendered == "" {
		return rendered
	}
	lines := strings.Split(rendered, "\n")
	lines[0] = SelectionPrefix + lines[0]
	return strings.Join(lines, "\n")
}

func RenderToolCall(tc transcript.ToolCallInfo, width int, focused bool) string {
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

	th := tuitheme.ActiveTheme()
	stateClr := stateColorForToolDone(tc.Done, tc.Error)
	bar := tuiprim.StateBar(stateClr)
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

	header := tuiprim.DotLeader(left, right, width)
	if focused {
		// Replace the single state bar with the double focus bar.
		header = strings.Replace(header, bar, tuiprim.FocusedStateBar(th.Accent), 1)
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
				errStyle := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateAwaiting)
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
			bodyLines = append(bodyLines, tuitheme.ToolExpandedStyle.Width(width-4).Render(tc.Detail))
		}
		if tc.Output != "" {
			bodyLines = append(bodyLines, tuitheme.ToolExpandedStyle.Width(width-4).Render(tc.Output))
		}
		if tc.Error != "" {
			bodyLines = append(bodyLines, tuitheme.ToolExpandedStyle.Width(width-4).Render("error: "+tc.Error))
		}
	}

	if len(bodyLines) == 0 {
		return header
	}
	return header + "\n" + strings.Join(bodyLines, "\n")
}

func stateColorForToolDone(done bool, errStr string) lipgloss.Color {
	th := tuitheme.ActiveTheme()
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
// transcript.ToolCallInfo.Description if present, else returns "".
// The existing transcript.ToolCallInfo.Description is a human summary or raw JSON.
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

// historyToMessages converts session history turns into TUI chat messages
// for display when resuming a session.
func historyToMessages(turns []schema.Turn) []transcript.ChatMessage {
	// Collect tool results keyed by call ID for matching with tool calls.
	toolResults := make(map[string]llm.ToolResultData)
	for _, t := range turns {
		if t.Kind != schema.TurnToolResults && t.Kind != schema.TurnTool {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				toolResults[p.ToolResult.ToolCallID] = *p.ToolResult
			}
		}
	}

	var msgs []transcript.ChatMessage
	for _, t := range turns {
		switch t.Kind {
		case schema.TurnUserInput:
			text := t.Message.Text()
			if strings.TrimSpace(text) != "" {
				msgs = append(msgs, transcript.ChatMessage{Kind: transcript.MsgUser, Text: text})
			}

		case schema.TurnAssistant:
			for _, p := range t.Message.Content {
				switch p.Kind {
				case llm.ContentText:
					// Skip empty text (common in tool-only responses).
					if strings.TrimSpace(p.Text) != "" {
						msgs = append(msgs, transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: p.Text})
					}

				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					tc := p.ToolCall
					if tc.Name == "communicate" {
						msg := extractCommunicate(tc)
						if msg != "" {
							msgs = append(msgs, transcript.ChatMessage{Kind: transcript.MsgCommunicate, Text: msg})
						}
						continue
					}

					// Non-communicate tool call: show as collapsed tool entry.
					argsJSON := string(tc.Arguments)
					toolDesc, toolDetail := toolsummary.SummarizeTool(tc.Name, argsJSON)
					result := toolResults[tc.ID]
					output := fmt.Sprintf("%v", result.Content)
					info := &transcript.ToolCallInfo{
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
					msgs = append(msgs, transcript.ChatMessage{Kind: transcript.MsgTool, Tool: info})
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
