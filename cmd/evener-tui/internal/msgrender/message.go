package msgrender

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitext"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

var markdownRenderer *glamour.TermRenderer
var markdownRendererWidth int

var newMarkdownRenderer = glamour.NewTermRenderer
var renderMarkdownWith = func(r *glamour.TermRenderer, text string) (string, error) {
	return r.Render(text)
}

func InitMarkdownRenderer(width int) {
	if width <= 0 {
		width = 80
	}
	style := themedGlamourStyle()
	r, err := newMarkdownRenderer(
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
	base.Code.BackgroundColor = new(bgRaised)
	base.Code.Color = new(text)

	// Code block container: deep-clone Chroma so we don't mutate the
	// package-level styles.LightStyleConfig / DarkStyleConfig.
	if base.CodeBlock.Chroma != nil {
		chromaCopy := *base.CodeBlock.Chroma
		chromaCopy.Background.BackgroundColor = new(bgRaised)
		chromaCopy.Background.Color = new(text)
		chromaCopy.Text.Color = new(text)
		base.CodeBlock.Chroma = &chromaCopy
	}
	base.CodeBlock.BackgroundColor = new(bgRaised)
	base.CodeBlock.Color = new(text)

	// Block quote: muted text in the theme tone.
	base.BlockQuote.Color = new(textMuted)

	return base
}

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
	rendered, err := renderMarkdownWith(markdownRenderer, text)
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
	for line := range strings.SplitSeq(text, "\n") {
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
	for line := range strings.SplitSeq(text, "\n") {
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
		bar := tuiprim.StateBar(th.StateWorking)
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
		// closest existing style; refine later if needed. A legacy human-note
		// update is persisted as a steering turn, so this body is another reader
		// that prints notes text: strip the terminal-boundary controls here.
		return RenderSelectedMessage(tuitheme.SystemStyle.Width(messageWidth).Render("↻ "+prefix+tuitext.StripControls(msg.Text)+suffix), focused)
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
	args, isJSONObject := toolArgsFromJSONObject(rawJSON)

	verb := r.Verb(args)
	target := r.Target(args)
	// When rawJSON is non-empty but does not parse as a JSON object (rejected
	// calls with malformed raw bytes), every renderer sees empty args and
	// produces an empty target. Mirror the hub's toolInputSummary bounded
	// fallback (agent/transcript_render.go:1595-1601) so the malformed raw
	// bytes reach the row instead of a bare verb.
	if rawJSON != "" && !isJSONObject {
		target = oneLineTrunc(rawJSON, toolCardRawFallbackMaxRunes)
	}
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
	// "purpose" was "intent"'s name before the 2026-08-29 rename
	// (7512a736e); fall back so pre-rename transcripts still show their
	// intent line (issue #709).
	intent := strings.TrimSpace(args.Str("intent"))
	if intent == "" {
		intent = strings.TrimSpace(args.Str("purpose"))
	}
	if intent != "" {
		line := lipgloss.NewStyle().Italic(true).Render(intent)
		bodyLines = append(bodyLines, indentBlock(line, th.IndentToolBody))
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
	if (tc.Name == "delegate" || tc.Name == "delegate_send") && tc.Subagent != nil {
		body := SubagentRunBody(*tc.Subagent, width-th.IndentToolBody)
		if tc.Name == "delegate_send" && body != "" {
			if reply := DelegateSendReplyBody(tc.Raw, tc.Output, width-th.IndentToolBody); reply != "" {
				body += "\n" + reply
			}
		}
		if body != "" {
			if tc.Error != "" {
				errStyle := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateAwaiting)
				body = body + "\n" + errStyle.Render(tc.Error)
			}
			bodyLines = append(bodyLines, indentBlock(body, th.IndentToolBody))
			bodyFromRenderer = true
		}
	}
	if r.Body != nil && !bodyFromRenderer {
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
	return th.StateWorking
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

// oneLineTrunc collapses newlines to spaces (so a multi-line raw payload does
// not span the chat view) and truncates to at most limit runes, appending an
// ellipsis when truncated. Mirrors the hub's oneLine+truncRunes bounding.
func oneLineTrunc(s string, limit int) string {
	// \r is stripped (not normalized to a space), matching the hub's oneLine
	// (agent/transcript_render.go:1762-1764): ReplaceAll(\n, " ") then
	// ReplaceAll(\r, ""). So "a\rb" -> "ab" and "a\r\nb" -> "a b".
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// toolCardRawFallbackMaxRunes bounds the raw-arguments fallback for a
// non-communicate tool-card row so one pathological line cannot dominate the
// chat view, mirroring the hub's toolInputSummary 120-rune bound for tool
// cards (agent/transcript_render.go:1600).
const toolCardRawFallbackMaxRunes = 120
