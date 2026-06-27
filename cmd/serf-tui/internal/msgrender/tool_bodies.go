package msgrender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
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
	th := tuitheme.ActiveTheme()
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
	if tuitheme.ActiveTheme().Name == "light" {
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
	th := tuitheme.ActiveTheme()
	// Strip exactly one terminal newline so a file ending with "\n" doesn't
	// register as having an empty trailing line (which would inflate the
	// preview count and trigger a bogus "show 1 more lines" hint).
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
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
	th := tuitheme.ActiveTheme()
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

// delegateBody renders a summary line for a delegate job.  At narrow
// widths (< 30 cols) only the summary is returned.  Full nested inline
// rendering of child transcripts is deferred to a follow-up kata.
//
// Metadata is sourced from the tool output (preferred) or args.
func delegateBody(args ToolArgs, output string, width int) string {
	var jobID, status string

	// Try to parse metadata from output first.
	if output != "" {
		var meta struct {
			JobID         string `json:"job_id"`
			Status        string `json:"status"`
			TranscriptRef string `json:"transcript_ref"`
			Task          string `json:"task"`
		}
		if err := json.Unmarshal([]byte(output), &meta); err == nil && meta.JobID != "" {
			jobID = meta.JobID
			status = meta.Status
		}
	}

	if jobID == "" {
		jobID = args.Str("job_id")
		status = args.Str("status")
	}

	if status == "" {
		status = "running"
	}

	th := tuitheme.ActiveTheme()
	summaryLabel := "delegate"
	if task := strings.TrimSpace(args.Str("task")); task != "" {
		summaryLabel = task
	}
	identity := shortID(jobID)
	if identity != "" {
		summaryLabel += " · job " + identity
	}
	summary := fmt.Sprintf("%s (%s)", summaryLabel, status)
	styled := lipgloss.NewStyle().Foreground(th.StateSubagent).Render(summary)

	if width < 30 {
		return styled // suppress nested body at narrow widths
	}

	// On-demand child transcript loading deferred to follow-up kata.
	return styled
}

func SubagentRunBody(run transcript.SubagentRunInfo, width int) string {
	status := strings.TrimSpace(run.Status)
	if status == "" {
		status = "running"
	}
	label := strings.TrimSpace(run.Task)
	if label == "" {
		label = "delegate"
	}
	parts := []string{label, "(" + status + ")"}
	if run.JobID != "" {
		parts = append(parts, "job "+shortID(run.JobID))
	}
	if run.DelegateID != "" && width >= 60 {
		parts = append(parts, "delegate "+shortID(run.DelegateID))
	}
	if run.TranscriptRef != "" && width >= 70 {
		parts = append(parts, "transcript "+run.TranscriptRef)
	}
	return lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateSubagent).Render(strings.Join(parts, " · "))
}

// RenderSubagentRail consolidates a contiguous run of subagent / background-job
// entries into one calm "delegation rail" block — the TUI analog of the web
// rail. A left rail glyph marks the block; a header tallies the workers; failures
// surface (red) and running entries list, while the finished pile recedes to a
// "✓ N done" count.
func RenderSubagentRail(runs []transcript.SubagentRunInfo, width int) string {
	if len(runs) == 0 {
		return ""
	}
	th := tuitheme.ActiveTheme()
	var running, done, failed []transcript.SubagentRunInfo
	for _, r := range runs {
		switch subagentRailClass(r.Status) {
		case "failed":
			failed = append(failed, r)
		case "done":
			done = append(done, r)
		default:
			running = append(running, r)
		}
	}
	rail := lipgloss.NewStyle().Foreground(th.RuleSoft).Render("│") + " "
	red := lipgloss.Color("9")

	tally := make([]string, 0, 3)
	if len(running) > 0 {
		tally = append(tally, fmt.Sprintf("⟳ %d running", len(running)))
	}
	if len(done) > 0 {
		tally = append(tally, fmt.Sprintf("✓ %d done", len(done)))
	}
	if len(failed) > 0 {
		tally = append(tally, fmt.Sprintf("✕ %d failed", len(failed)))
	}
	header := "Subagents"
	if len(tally) > 0 {
		header += " · " + strings.Join(tally, " · ")
	}

	lines := []string{rail + lipgloss.NewStyle().Foreground(th.TextMuted).Bold(true).Render(header)}
	for _, r := range failed { // failures surface at the top
		lines = append(lines, rail+subagentRailRow(r, "✕", red, width))
	}
	for _, r := range running {
		lines = append(lines, rail+subagentRailRow(r, "⟳", th.StateSubagent, width))
	}
	if len(done) > 0 { // the settled pile recedes to a count
		lines = append(lines, rail+lipgloss.NewStyle().Foreground(th.TextMuted).Render(fmt.Sprintf("✓ %d done", len(done))))
	}
	return strings.Join(lines, "\n")
}

func subagentRailRow(r transcript.SubagentRunInfo, glyph string, glyphColor lipgloss.TerminalColor, width int) string {
	name := strings.TrimSpace(r.Task)
	if name == "" {
		name = strings.TrimSpace(r.Command)
	}
	if name == "" {
		name = "delegate"
	}
	limit := width - 6
	if limit < 12 {
		limit = 12
	}
	if len(name) > limit {
		name = name[:limit-1] + "…"
	}
	th := tuitheme.ActiveTheme()
	row := lipgloss.NewStyle().Foreground(glyphColor).Render(glyph) + " " + name
	switch subagentRailClass(r.Status) {
	case "running":
		// The live activity line: the child's latest step + an honest step count
		// (it freezes when the child stalls — no fake liveness).
		if act := strings.TrimSpace(r.Activity); act != "" {
			if len(act) > 50 {
				act = act[:49] + "…"
			}
			if r.Steps > 0 {
				act = fmt.Sprintf("%s · %d", act, r.Steps)
			}
			row += "  " + lipgloss.NewStyle().Foreground(th.TextDim).Render(act)
		}
	case "failed":
		if reason := strings.TrimSpace(r.Reason); reason != "" {
			if len(reason) > 40 {
				reason = reason[:39] + "…"
			}
			row += "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(reason)
		}
	}
	return row
}

func subagentRailClass(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return "failed"
	case "completed", "done", "succeeded", "cancelled", "stopped":
		return "done"
	default:
		return "running"
	}
}

// ShellBody renders shell command output, optionally with bash chroma highlighting.
// It prepends the command (from args) as a styled prompt line so that long or
// multi-line commands are visible in the expanded view.
func ShellBody(args ToolArgs, output string, width int) string {
	var lines []string
	if cmd := strings.TrimSpace(args.Str("command")); cmd != "" {
		cmdStyled := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().TextMuted).Render("$ " + cmd)
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
