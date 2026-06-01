package tuiprim

import (
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitext"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func RenderStyledPane(text string, width int) string {
	if width <= 0 {
		return ""
	}
	innerWidth := max(1, width-3)
	body := tuitext.TruncateMultilineText(text, innerWidth)
	return tuitheme.DefaultTUIStyles().Pane.Width(width).Render(body)
}

func RenderPopupPane(text string, width int) string {
	if width <= 0 {
		width = 96
	}
	terminalWidth := width
	popupWidth := PopupPaneWidth(width)
	innerWidth := PopupPaneContentWidth(width)
	body := tuitext.TruncateMultilineText(text, innerWidth)
	pane := tuitheme.DefaultTUIStyles().Modal.Width(popupWidth).Render(body)
	if terminalWidth > popupWidth {
		return lipgloss.PlaceHorizontal(terminalWidth, lipgloss.Center, pane)
	}
	return pane
}

// PopupPaneWidth returns the outer width of a popup pane rendered into a
// terminal of the given width. Mirrors the clamp inside RenderPopupPane so
// callers that need to lay out text for the popup can wrap at the same width
// the pane will actually render at.
func PopupPaneWidth(termWidth int) int {
	if termWidth <= 0 {
		termWidth = 96
	}
	return min(max(termWidth, 44), 96)
}

// PopupPaneContentWidth returns the inner content width (i.e. after the
// pane's horizontal padding/border) for a popup at the given terminal width.
// Use this when sizing wrapped text destined for RenderPopupPane so long
// lines wrap inside the pane instead of being truncated by the pane.
func PopupPaneContentWidth(termWidth int) int {
	return max(1, PopupPaneWidth(termWidth)-6)
}
