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
	// Right-side status. Previously lived in a separate persistent
	// status bar below the composer; consolidated here so the chip
	// strip is the single live-context line.
	Connected bool
	HubAddr   string
	Provider  string
	Width     int
}

// renderComposerChipStrip renders a horizontal chip strip above the composer
// textarea. Left side: harness/model/branch/dir chips separated by ·.
// Right side: connection status, then mode chip (state-colored via
// StatusBadge) when Mode is non-empty. The whole line is laid out as a
// divider: ─ <chips> ──…──── [status]  [mode] ┄
func renderComposerChipStrip(ctx composerContext) string {
	th := activeTheme()

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

	// Right-side status fragment.
	rightParts := []string{}
	if status := renderChipStatus(ctx, th); status != "" {
		rightParts = append(rightParts, status)
	}
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
		rightParts = append(rightParts, StatusBadge(modeColor, ctx.Mode))
	}
	rightContent := strings.Join(rightParts, "  ")

	// Build the divider line manually so that chip labels are NOT uppercased
	// (SectionDivider upper-cases its left argument).
	width := ctx.Width
	if width <= 0 {
		width = 80
	}
	leadGlyph := lipgloss.NewStyle().Foreground(th.RuleSoft).Render("─ ")
	trailGlyph := lipgloss.NewStyle().Foreground(th.Rule).Render(" " + activeTheme().RuleGlyph)

	prefix := leadGlyph + chipsText
	suffix := rightContent + trailGlyph
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

// renderChipStatus produces the right-side connection/provider fragment
// of the composer chip strip. Returns "" when there is no connection
// context to surface.
func renderChipStatus(ctx composerContext, th Theme) string {
	if ctx.HubAddr == "" && !ctx.Connected && ctx.Provider == "" {
		return ""
	}
	var fragments []string
	healthClr := th.StateAwaiting
	healthLabel := "disconnected"
	if ctx.Connected {
		healthClr = th.StateIdle
		healthLabel = "connected"
	}
	health := lipgloss.NewStyle().Foreground(healthClr).Bold(true).Render("●") +
		" " + lipgloss.NewStyle().Foreground(th.TextDim).Render(healthLabel)
	fragments = append(fragments, health)
	if ctx.HubAddr != "" {
		fragments = append(fragments, lipgloss.NewStyle().Foreground(th.TextDim).Render(ctx.HubAddr))
	}
	if ctx.Provider != "" {
		fragments = append(fragments, lipgloss.NewStyle().Foreground(th.TextDim).Render(ctx.Provider))
	}
	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	return strings.Join(fragments, sep)
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
