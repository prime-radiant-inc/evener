package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

const compactThreshold = 0.90 // must match agent/context_manager.go SummarizeThreshold

func renderStatusBar(connected bool, model, sessionID string, turns, contextTokens, contextWindowSize int, processing bool, turnIn, turnOut int, scrollMode bool, width int) string {
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
		right = fmt.Sprintf("model: %s  turns: %d", model, turns)
		if processing && (turnIn > 0 || turnOut > 0) {
			right += fmt.Sprintf("  ↑%s ↓%s", formatTokens(turnIn), formatTokens(turnOut))
		}
		if contextTokens > 0 && contextWindowSize > 0 {
			pct := float64(contextTokens) / float64(contextWindowSize) * 100
			compactAt := int(float64(contextWindowSize) * compactThreshold)
			remaining := compactAt - contextTokens
			if remaining < 0 {
				remaining = 0
			}
			right += fmt.Sprintf("  ctx: %s/%s (%0.f%%, %s to compact)",
				formatTokens(contextTokens),
				formatTokens(contextWindowSize),
				pct,
				formatTokens(remaining))
		} else if contextTokens > 0 {
			right += fmt.Sprintf("  ctx: %s", formatTokens(contextTokens))
		}
		right += " "
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	content := left + fmt.Sprintf("%*s", gap, "") + right
	return statusBarStyle.Width(width).Render(content)
}

// formatTokens formats a token count compactly: e.g. 1234 → "1.2k", 12345 → "12k".
func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%dk", n/1000)
}
