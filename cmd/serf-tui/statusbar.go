package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

func renderStatusBar(connected bool, model, sessionID string, turns, width int) string {
	var connIndicator string
	if connected {
		connIndicator = statusConnected.Render("● connected")
	} else {
		connIndicator = statusDisconnected.Render("○ disconnected")
	}

	left := fmt.Sprintf("serf %s", connIndicator)
	right := ""
	if model != "" {
		right = fmt.Sprintf("model: %s  turns: %d", model, turns)
	}

	// statusBarStyle has Padding(0,1) which adds 1 col on each side.
	// Use Width(width-2) so that content-width + 2-padding-cols = terminal width.
	innerWidth := width - 2
	if innerWidth < 2 {
		innerWidth = 2
	}

	gap := innerWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	content := left + fmt.Sprintf("%*s", gap, "") + right
	return statusBarStyle.Width(innerWidth).Render(content)
}
