package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// diffBody renders a unified diff with per-line state tints:
//   - "+" lines → StateIdleTint background (green)
//   - "-" lines → StateAwaitingTint background (red)
//   - "@@" lines → StateWarning foreground, bold
//   - context lines → plain Text foreground
func diffBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	th := activeTheme()
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var styled string
		switch {
		case strings.HasPrefix(line, "@@"):
			styled = lipgloss.NewStyle().Foreground(th.StateWarning).Bold(true).Render(line)
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			styled = lipgloss.NewStyle().
				Background(th.StateIdleTint).
				Foreground(th.StateIdle).
				Render(line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			styled = lipgloss.NewStyle().
				Background(th.StateAwaitingTint).
				Foreground(th.StateAwaiting).
				Render(line)
		default:
			styled = lipgloss.NewStyle().Foreground(th.Text).Render(line)
		}
		out = append(out, styled)
	}
	return strings.Join(out, "\n")
}

// chromaStyleForActiveTheme returns the chroma style name that matches
// the active TUI theme. monokai is a dark style — its pale syntax colors
// render as near-white on light terminals. github is a light-mode style
// with strong contrast on light backgrounds.
func chromaStyleForActiveTheme() string {
	if activeTheme().Name == "light" {
		return "github"
	}
	return "monokai"
}

// highlightBlockByFilename returns chroma-highlighted text for the language
// inferred from filename, or empty string on any failure.
func highlightBlockByFilename(text, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	style := styles.Get(chromaStyleForActiveTheme())
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return ""
	}
	iter, err := lexer.Tokenise(nil, text)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iter); err != nil {
		return ""
	}
	return buf.String()
}

// highlightBlock returns chroma-highlighted text for the given language name,
// or empty string on any failure (unknown language, tokenise error, etc.).
func highlightBlock(text, lang string) string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		return ""
	}
	style := styles.Get(chromaStyleForActiveTheme())
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return ""
	}
	iter, err := lexer.Tokenise(nil, text)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iter); err != nil {
		return ""
	}
	return buf.String()
}

// chromaHighlight applies syntax highlighting via chroma, using the file
// extension of filename to select the lexer.  Falls back to plain text.
func chromaHighlight(text, filename string) string {
	if h := highlightBlockByFilename(text, filename); h != "" {
		return h
	}
	return text
}

// fileBodyPreviewLines is the number of lines shown before truncation.
const fileBodyPreviewLines = 5

// fileBody renders the first fileBodyPreviewLines lines of a file, with
// chroma syntax highlighting by filename extension.  If there are more
// lines, a "▸ show N more lines" hint is appended.
func fileBody(args ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	th := activeTheme()
	lines := strings.Split(output, "\n")
	preview := lines
	more := 0
	if len(lines) > fileBodyPreviewLines {
		preview = lines[:fileBodyPreviewLines]
		more = len(lines) - fileBodyPreviewLines
	}

	highlighted := chromaHighlight(strings.Join(preview, "\n"), args.Str("file_path"))

	if more > 0 {
		hint := lipgloss.NewStyle().Foreground(th.TextDim).Render(fmt.Sprintf("▸ show %d more lines", more))
		return highlighted + "\n" + hint
	}
	return highlighted
}

// taskItem is the JSON shape emitted by the task_list tool's output.
type taskItem struct {
	Description string `json:"description"`
	Name        string `json:"name"` // fallback for older shapes
	Status      string `json:"status"`
}

// taskListBody renders a list of tasks with per-status glyphs and colors.
func taskListBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	var items []taskItem
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		return ""
	}
	th := activeTheme()
	lines := make([]string, 0, len(items))
	for _, item := range items {
		var glyph string
		var clr lipgloss.Color
		switch item.Status {
		case "done":
			glyph = "[✓]"
			clr = th.StateIdle
		case "in_progress":
			glyph = "[⠋]"
			clr = th.StateProcessing
		default:
			glyph = "[ ]"
			clr = th.TextDim
		}
		label := item.Description
		if label == "" {
			label = item.Name
		}
		g := lipgloss.NewStyle().Foreground(clr).Render(glyph)
		name := lipgloss.NewStyle().Foreground(th.Text).Render(label)
		lines = append(lines, g+" "+name)
	}
	return strings.Join(lines, "\n")
}

// subagentBody renders a summary line for a spawned subagent.  At narrow
// widths (< 30 cols) only the summary is returned.  Full nested inline
// rendering of child transcripts is deferred to a follow-up kata.
//
// Metadata is sourced from the tool output (preferred) or args (fallback for
// older shapes where spawn_agent put metadata in args instead of output).
func subagentBody(args ToolArgs, output string, width int) string {
	var agentID, status string
	var turns int

	// Try to parse metadata from output first.
	if output != "" {
		var meta struct {
			AgentID   string `json:"agent_id"`
			Status    string `json:"status"`
			TurnsUsed int    `json:"turns_used"`
			SessionID string `json:"session_id"`
			Task      string `json:"task"`
		}
		if err := json.Unmarshal([]byte(output), &meta); err == nil && (meta.AgentID != "" || meta.SessionID != "") {
			agentID = meta.AgentID
			if agentID == "" {
				agentID = meta.SessionID
			}
			status = meta.Status
			turns = meta.TurnsUsed
		}
	}

	// Fall back to args for older shapes.
	if agentID == "" {
		agentID = args.Str("agent_id")
		if v, ok := args["turns_used"].(float64); ok {
			turns = int(v)
		}
		status = args.Str("status")
	}

	if status == "" {
		status = "running"
	}

	th := activeTheme()
	summary := fmt.Sprintf("subagent %s (%d turns, %s)", shortID(agentID), turns, status)
	styled := lipgloss.NewStyle().Foreground(th.StateSubagent).Render(summary)

	if width < 30 {
		return styled // suppress nested body at narrow widths
	}

	// On-demand child transcript loading deferred to follow-up kata.
	return styled
}

// shellBody renders shell command output, optionally with bash chroma highlighting.
// It prepends the command (from args) as a styled prompt line so that long or
// multi-line commands are visible in the expanded view.
func shellBody(args ToolArgs, output string, width int) string {
	var lines []string
	if cmd := strings.TrimSpace(args.Str("command")); cmd != "" {
		cmdStyled := lipgloss.NewStyle().Foreground(activeTheme().TextMuted).Render("$ " + cmd)
		lines = append(lines, cmdStyled)
	}
	if output != "" {
		highlighted := highlightBlock(output, "bash")
		if highlighted != "" {
			lines = append(lines, highlighted)
		} else {
			lines = append(lines, output)
		}
	}
	return strings.Join(lines, "\n")
}

// webSearchBody passes through web search output unchanged.
func webSearchBody(_ ToolArgs, output string, width int) string {
	return output
}

// jsonBody renders pretty-printed, chroma-highlighted JSON output.
func jsonBody(_ ToolArgs, output string, width int) string {
	if output == "" {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(output), "", "  "); err != nil {
		return output
	}
	if h := highlightBlock(pretty.String(), "json"); h != "" {
		return h
	}
	return pretty.String()
}
