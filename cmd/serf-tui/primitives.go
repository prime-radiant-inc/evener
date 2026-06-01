package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

// StateBar returns a single 1-column glyph foreground-colored to state.
func StateBar(state lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(state).Render(tuitheme.ActiveTheme().LeftBarGlyph)
}

// FocusedStateBar returns the same glyph twice for selected/focused rows.
// Total visual width is 2 columns; callers must account for this in
// right-alignment math.
func FocusedStateBar(state lipgloss.Color) string {
	g := tuitheme.ActiveTheme().LeftBarGlyph
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
	th := tuitheme.ActiveTheme()
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
		// Narrow: drop the fill mid-section and truncate on unstyled text
		// before applying styles so we never slice through ANSI escapes.
		combined := prefix + " " + suffix
		if lipgloss.Width(combined) > width {
			// Build the plain (unstyled) equivalent to measure and truncate.
			plainLeft := strings.ToUpper(left)
			plainRight := right
			plain := "─ " + plainLeft + " " + plainRight + " " + th.RuleGlyph
			// Truncate the plain version, then re-render truncated parts with styles.
			if len([]rune(plain)) > width {
				// Keep as much of the left label as possible, then the trail glyph.
				trailPlain := " " + th.RuleGlyph
				trailW := lipgloss.Width(trailPlain)
				leadPlain := "─ "
				leadW := lipgloss.Width(leadPlain)
				available := width - leadW - trailW
				if available < 0 {
					available = 0
				}
				truncatedLeft := truncateText(strings.ToUpper(left), available)
				return lipgloss.NewStyle().Foreground(th.RuleSoft).Render("─ ") +
					lipgloss.NewStyle().Foreground(th.TextDim).Bold(true).Render(truncatedLeft) +
					lipgloss.NewStyle().Foreground(th.Rule).Render(trailPlain)
			}
		}
		return combined
	}
	mid := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(strings.Repeat("─", fill))
	return prefix + " " + mid + " " + suffix
}

// KbdHint renders "key action" — key in bold Accent, action in
// TextDim. No background fill: a reverse-video pill reads as a hard
// black/white rectangle that fights the surrounding paper, and the
// typography alone is enough to mark the key apart from the action.
func KbdHint(key, action string) string {
	th := tuitheme.ActiveTheme()
	keyStyled := lipgloss.NewStyle().
		Foreground(th.Accent).
		Bold(true).
		Render(key)
	actionStyled := lipgloss.NewStyle().Foreground(th.TextDim).Render(action)
	return keyStyled + " " + actionStyled
}

// DotLeader returns "left ········ right" exactly `width` columns wide
// (best-effort). Dots are TextGhost color.
func DotLeader(left, right string, width int) string {
	th := tuitheme.ActiveTheme()
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if width <= 0 {
		return left + " " + right
	}
	fill := width - lw - rw - 2
	if fill < 1 {
		maxLeft := width - rw - 2
		if maxLeft < 1 {
			return ansi.Truncate(right, width, "…")
		}
		return ansi.Truncate(left, maxLeft, "…") + "  " + right
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
	th := tuitheme.ActiveTheme()
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

	// 4 = 2 padding columns on each side from Padding(1,2) above.
	contentWidth := opts.Width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	titleLine := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(opts.Title)
	body := ansi.Wrap(opts.Body, contentWidth, "")
	if opts.Footer != "" {
		footer := ansi.Truncate(opts.Footer, contentWidth, "…")
		body += "\n\n" + lipgloss.NewStyle().Foreground(th.TextDim).Render(footer)
	}
	content := titleLine + "\n\n" + body
	return frame.Render(content)
}
