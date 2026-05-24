package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// StateBar returns a single 1-column glyph foreground-colored to state.
func StateBar(state lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(state).Render(activeThemeV2().LeftBarGlyph)
}

// FocusedStateBar returns the same glyph twice for selected/focused rows.
// Total visual width is 2 columns; callers must account for this in
// right-alignment math.
func FocusedStateBar(state lipgloss.Color) string {
	g := activeThemeV2().LeftBarGlyph
	return lipgloss.NewStyle().Foreground(state).Render(g + g)
}

// StatusBadge renders a reverse-pill: bold uppercase label with a leading
// dot, foreground in state color.
func StatusBadge(state lipgloss.Color, label string) string {
	upper := strings.ToUpper(label)
	return lipgloss.NewStyle().
		Foreground(state).
		Bold(true).
		Render("● " + upper)
}

// SectionDivider renders "─ LEFT ──…────── RIGHT ┄" filling middle with
// theme.Rule, label tone via theme.TextDim, trailing ┄ in theme.Rule.
func SectionDivider(width int, left, right string) string {
	th := activeThemeV2()
	if width <= 0 {
		width = 60
	}
	leftStyled := lipgloss.NewStyle().Foreground(th.TextDim).Bold(true).Render(strings.ToUpper(left))
	rightStyled := lipgloss.NewStyle().Foreground(th.TextGhost).Render(right)
	leadGlyph := lipgloss.NewStyle().Foreground(th.RuleSoft).Render("─ ")
	trailGlyph := lipgloss.NewStyle().Foreground(th.Rule).Render(" " + th.RuleGlyph)

	prefix := leadGlyph + leftStyled
	suffix := rightStyled + trailGlyph
	prefixW := lipgloss.Width(prefix)
	suffixW := lipgloss.Width(suffix)
	fill := width - prefixW - suffixW - 2
	if fill < 1 {
		return prefix + " " + suffix
	}
	mid := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(strings.Repeat("─", fill))
	return prefix + " " + mid + " " + suffix
}

// KbdHint renders "<reverse-key> action" — key in reverse video,
// action in TextDim.
func KbdHint(key, action string) string {
	th := activeThemeV2()
	keyStyled := lipgloss.NewStyle().
		Reverse(true).
		Foreground(th.Text).
		Padding(0, 1).
		Render(key)
	actionStyled := lipgloss.NewStyle().Foreground(th.TextDim).Render(action)
	return keyStyled + " " + actionStyled
}

// DotLeader returns "left ········ right" exactly `width` columns wide
// (best-effort). Dots are TextGhost color.
func DotLeader(left, right string, width int) string {
	th := activeThemeV2()
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if width <= 0 {
		return left + " " + right
	}
	fill := width - lw - rw - 2
	if fill < 1 {
		maxLeft := width - rw - 2
		if maxLeft < 1 {
			return truncateText(right, width)
		}
		return truncateText(left, maxLeft) + "  " + right
	}
	dots := lipgloss.NewStyle().Foreground(th.TextGhost).Render(strings.Repeat("·", fill))
	return left + " " + dots + " " + right
}

// OverlayOpts holds configuration for Overlay.
type OverlayOpts struct {
	Title  string
	Width  int
	Body   string
	Footer string
	Accent lipgloss.Color
}

// Overlay renders a rounded-border modal with title, body, and optional footer.
func Overlay(opts OverlayOpts) string {
	th := activeThemeV2()
	accent := opts.Accent
	if accent == "" {
		accent = th.Accent
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}

	border := lipgloss.RoundedBorder()
	frame := lipgloss.NewStyle().
		Border(border).
		BorderForeground(accent).
		Foreground(th.Text).
		Background(th.BgRaised).
		Padding(1, 2).
		Width(opts.Width)

	titleLine := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(opts.Title)
	body := opts.Body
	if opts.Footer != "" {
		body += "\n\n" + lipgloss.NewStyle().Foreground(th.TextDim).Render(opts.Footer)
	}
	content := titleLine + "\n\n" + body
	return frame.Render(content)
}
