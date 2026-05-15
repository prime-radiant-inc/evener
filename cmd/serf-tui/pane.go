package main

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
	width = min(max(width, 44), 96)
	return renderStyledPane(text, width)
}
