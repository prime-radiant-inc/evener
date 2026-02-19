package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

func renderStatusBar(connected bool, model, sessionID string, turns int, scrollMode bool, width int) string {
	var connIndicator string
	if connected {
		connIndicator = statusConnected.Render("● connected")
	} else {
		connIndicator = statusDisconnected.Render("○ disconnected")
	}

	left := " serf " + connIndicator
	right := ""
	if scrollMode {
		right = scrollModeStyle.Render(" SCROLL  esc to exit ")
	} else if model != "" {
		right = fmt.Sprintf("model: %s  turns: %d ", model, turns)
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	content := left + fmt.Sprintf("%*s", gap, "") + right
	return statusBarStyle.Width(width).Render(content)
}
