package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type appShell struct {
	TopBar  string
	Body    string
	Overlay string
	Footer  string
	Height  int
}

func (s appShell) View() string {
	contentSections := make([]string, 0, 3)
	styles := tuitheme.DefaultTUIStyles()
	if topBar := strings.TrimRight(s.TopBar, "\n"); topBar != "" {
		contentSections = append(contentSections, styles.Title.Render(topBar))
	}
	if overlay := strings.TrimRight(s.Overlay, "\n"); overlay != "" {
		contentSections = append(contentSections, overlay)
	}
	if body := strings.TrimRight(s.Body, "\n"); body != "" {
		contentSections = append(contentSections, body)
	}
	footer := strings.TrimRight(s.Footer, "\n")
	if len(contentSections) == 0 && footer == "" {
		return ""
	}
	content := strings.Join(contentSections, "\n\n")
	if footer == "" {
		return content + "\n"
	}
	if s.Height <= 0 {
		if content == "" {
			return footer + "\n"
		}
		return content + "\n\n" + footer + "\n"
	}
	if content == "" {
		gap := max(0, s.Height-shellSectionLineCount(footer))
		return strings.Repeat("\n", gap) + footer
	}
	gap := s.Height - shellSectionLineCount(content) - shellSectionLineCount(footer) + 1
	if gap < 2 {
		gap = 2
	}
	return content + strings.Repeat("\n", gap) + footer
}

func actionBar(keys ...string) string {
	return strings.Join(keys, "  ")
}

func actionBarForWidth(width int, keys ...string) string {
	if width <= 0 {
		return actionBar(keys...)
	}
	lines := make([]string, 0, 1)
	current := ""
	for _, key := range keys {
		if current == "" {
			current = key
			continue
		}
		next := current + "  " + key
		if lipgloss.Width(next) > width {
			lines = append(lines, current)
			current = key
			continue
		}
		current = next
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}
