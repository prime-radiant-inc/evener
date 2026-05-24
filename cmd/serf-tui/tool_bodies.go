package main

import (
	"strings"

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
