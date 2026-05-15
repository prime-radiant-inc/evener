package main

import "github.com/charmbracelet/lipgloss"

func renderStyledPane(text string, width int) string {
	if width <= 0 {
		return ""
	}
	innerWidth := max(1, width-3)
	body := truncateMultilineText(text, innerWidth)
	return defaultTUIStyles().Pane.Width(width).Render(body)
}

func renderPopupPane(text string, width int) string {
	if width <= 0 {
		width = 96
	}
	terminalWidth := width
	popupWidth := min(max(width, 44), 96)
	innerWidth := max(1, popupWidth-6)
	body := truncateMultilineText(text, innerWidth)
	pane := defaultTUIStyles().Modal.Width(popupWidth).Render(body)
	if terminalWidth > popupWidth {
		return lipgloss.PlaceHorizontal(terminalWidth, lipgloss.Center, pane)
	}
	return pane
}
