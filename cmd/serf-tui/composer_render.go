package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// composerContext holds the metadata used to render the composer chip strip.
type composerContext struct {
	Harness    string
	Model      string
	Branch     string
	WorkingDir string
	Mode       string // QUEUE 2, FORK DRAFT, AWAITING, etc.; empty for default compose
	Width      int
}

// renderComposerChipStrip renders a horizontal chip strip above the composer
// textarea. Left side: harness/model/branch/dir chips separated by ·.
// Right side: mode chip (state-colored via StatusBadge) when Mode is non-empty.
// The whole line is laid out as a divider: ─ <chips> ──…──── <mode> ┄
func renderComposerChipStrip(ctx composerContext) string {
	th := activeThemeV2()

	parts := []string{}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k := lipgloss.NewStyle().Foreground(th.TextDim).Render(key)
		v := lipgloss.NewStyle().Foreground(th.Text).Render(value)
		parts = append(parts, k+" "+v)
	}
	add("harness", ctx.Harness)
	add("model", abbreviateModel(ctx.Model))
	add("branch", ctx.Branch)
	if ctx.WorkingDir != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(abbreviatePath(ctx.WorkingDir, 32)))
	}

	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	chipsText := strings.Join(parts, sep)

	var modeChip string
	if ctx.Mode != "" {
		modeColor := th.Accent
		switch {
		case strings.HasPrefix(ctx.Mode, "QUEUE"):
			modeColor = th.StateProcessing
		case strings.HasPrefix(ctx.Mode, "FORK"):
			modeColor = th.StateWarning
		case strings.HasPrefix(ctx.Mode, "AWAITING"):
			modeColor = th.StateAwaiting
		}
		modeChip = StatusBadge(modeColor, ctx.Mode)
	}

	// Build the divider line manually so that chip labels are NOT uppercased
	// (SectionDivider upper-cases its left argument).
	width := ctx.Width
	if width <= 0 {
		width = 80
	}
	leadGlyph := lipgloss.NewStyle().Foreground(th.RuleSoft).Render("─ ")
	trailGlyph := lipgloss.NewStyle().Foreground(th.Rule).Render(" " + activeThemeV2().RuleGlyph)

	prefix := leadGlyph + chipsText
	suffix := modeChip + trailGlyph
	prefixW := lipgloss.Width(prefix)
	suffixW := lipgloss.Width(suffix)
	fill := width - prefixW - suffixW - 2
	if fill < 1 {
		combined := prefix + " " + suffix
		if lipgloss.Width(combined) > width {
			return truncateText(combined, width)
		}
		return combined
	}
	mid := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(strings.Repeat("─", fill))
	return prefix + " " + mid + " " + suffix
}

// composerFooterHints returns the mode-appropriate keyboard hint bar.
// canSteer controls whether the ctrl+s steer hint is included in queue mode.
func composerFooterHints(mode string, width int, canSteer bool) string {
	switch mode {
	case "queue":
		hints := []string{KbdHint("enter", "queue")}
		if canSteer {
			hints = append(hints, KbdHint("ctrl+s", "steer"))
		}
		hints = append(hints,
			KbdHint("esc", "browse"),
			KbdHint("⌘P", "palette"),
			KbdHint("⌘O", "dashboard"),
		)
		return actionBarForWidth(width, hints...)
	case "fork":
		return actionBarForWidth(width,
			KbdHint("enter", "fork"),
			KbdHint("esc", "cancel"),
			KbdHint("⌘O", "dashboard"),
		)
	case "scroll-browse":
		return actionBarForWidth(width,
			KbdHint("↑↓", "select"),
			KbdHint("enter", "expand"),
			KbdHint("f", "fork"),
			KbdHint("c", "copy"),
			KbdHint("esc", "compose"),
			KbdHint("⌘O", "dashboard"),
		)
	default: // compose
		return actionBarForWidth(width,
			KbdHint("enter", "send"),
			KbdHint("shift+enter", "newline"),
			KbdHint("tab", "toggle last tool"),
			KbdHint("⌘P", "palette"),
			KbdHint("esc", "browse"),
			KbdHint("/help", ""),
		)
	}
}
