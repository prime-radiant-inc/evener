package main

import (
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func renderStyledPane(text string, width int) string {
	if width <= 0 {
		return ""
	}
	innerWidth := max(1, width-3)
	body := truncateMultilineText(text, innerWidth)
	return tuitheme.DefaultTUIStyles().Pane.Width(width).Render(body)
}

func renderPopupPane(text string, width int) string {
	if width <= 0 {
		width = 96
	}
	terminalWidth := width
	popupWidth := popupPaneWidth(width)
	innerWidth := popupPaneContentWidth(width)
	body := truncateMultilineText(text, innerWidth)
	pane := tuitheme.DefaultTUIStyles().Modal.Width(popupWidth).Render(body)
	if terminalWidth > popupWidth {
		return lipgloss.PlaceHorizontal(terminalWidth, lipgloss.Center, pane)
	}
	return pane
}

// popupPaneWidth returns the outer width of a popup pane rendered into a
// terminal of the given width. Mirrors the clamp inside renderPopupPane so
// callers that need to lay out text for the popup can wrap at the same width
// the pane will actually render at.
func popupPaneWidth(termWidth int) int {
	if termWidth <= 0 {
		termWidth = 96
	}
	return min(max(termWidth, 44), 96)
}

// popupPaneContentWidth returns the inner content width (i.e. after the
// pane's horizontal padding/border) for a popup at the given terminal width.
// Use this when sizing wrapped text destined for renderPopupPane so long
// lines wrap inside the pane instead of being truncated by the pane.
func popupPaneContentWidth(termWidth int) int {
	return max(1, popupPaneWidth(termWidth)-6)
}
