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
	th := activeThemeV2()
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

// highlightBlockByFilename returns chroma-highlighted text for the language
// inferred from filename, or empty string on any failure.
func highlightBlockByFilename(text, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	style := styles.Get("monokai")
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
	style := styles.Get("monokai")
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
	th := activeThemeV2()
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
	Name   string `json:"name"`
	Status string `json:"status"`
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
	th := activeThemeV2()
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
		g := lipgloss.NewStyle().Foreground(clr).Render(glyph)
		name := lipgloss.NewStyle().Foreground(th.Text).Render(item.Name)
		lines = append(lines, g+" "+name)
	}
	return strings.Join(lines, "\n")
}
