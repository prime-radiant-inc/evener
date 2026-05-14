package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type appShell struct {
	TopBar  string
	Body    string
	Overlay string
	Footer  string
}

func (s appShell) View() string {
	sections := make([]string, 0, 4)
	styles := defaultTUIStyles()
	if topBar := strings.TrimRight(s.TopBar, "\n"); topBar != "" {
		sections = append(sections, styles.Title.Render(topBar))
	}
	if body := strings.TrimRight(s.Body, "\n"); body != "" {
		sections = append(sections, body)
	}
	if overlay := strings.TrimRight(s.Overlay, "\n"); overlay != "" {
		sections = append(sections, overlay)
	}
	if footer := strings.TrimRight(s.Footer, "\n"); footer != "" {
		sections = append(sections, footer)
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n"
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
