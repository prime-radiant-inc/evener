package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/modeldisplay"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
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

// renderComposerChipStrip renders the live-context band above the composer
// textarea. Left: harness/model/branch/dir chips separated by ·. Right:
// connection status and, when in queue/fork/awaiting mode, a state-colored
// mode chip. The whole line is painted as a solid SurfaceSecondary band.
//
// Every inner styled span explicitly sets Background(SurfaceSecondary) so
// the band's bg paints through cleanly: nested lipgloss spans emit ANSI
// resets at their boundaries, and unstyled join glue (separators, gap
// spaces) would otherwise drop back to the terminal default between spans.
func renderComposerChipStrip(ctx composerContext) string {
	th := tuitheme.ActiveTheme()
	bg := th.SurfaceSecondary

	// All inner spans must declare the band bg.
	dim := lipgloss.NewStyle().Background(bg).Foreground(th.TextDim)
	text := lipgloss.NewStyle().Background(bg).Foreground(th.Text)
	ghost := lipgloss.NewStyle().Background(bg).Foreground(th.TextGhost)
	bgOnly := lipgloss.NewStyle().Background(bg)

	// Left side: harness/model/branch/dir
	leftParts := []string{}
	add := func(key, value string) {
		if value == "" {
			return
		}
		leftParts = append(leftParts, dim.Render(key)+bgOnly.Render(" ")+text.Render(value))
	}
	add("harness", ctx.Harness)
	add("model", composeProviderModel(ctx.Provider, ctx.Model))
	add("branch", ctx.Branch)
	if ctx.WorkingDir != "" {
		leftParts = append(leftParts, dim.Render(modeldisplay.AbbreviatePath(ctx.WorkingDir, 32)))
	}
	sep := ghost.Render(" · ")
	leftContent := strings.Join(leftParts, sep)

	// Right side: status + optional mode chip
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
		// Build a mode chip inline with the band bg (instead of using
		// the shared StatusBadge, which has no band bg).
		mode := lipgloss.NewStyle().Background(bg).Foreground(modeColor).Bold(true).Render("● " + strings.ToUpper(ctx.Mode))
		rightParts = append(rightParts, mode)
	}
	rightContent := strings.Join(rightParts, bgOnly.Render("  "))

	width := ctx.Width
	if width <= 0 {
		width = 80
	}

	band := lipgloss.NewStyle().
		Background(bg).
		Foreground(th.Text).
		Width(width).
		Padding(0, 1)

	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	leftW := lipgloss.Width(leftContent)
	rightW := lipgloss.Width(rightContent)
	if leftW+rightW+1 > inner {
		// Chip-strip fragments are ANSI-styled (each span declares the band
		// bg explicitly to survive the parent's reset boundaries). Use
		// ansi.Truncate so the underlying SGR escapes stay intact and the
		// band bg keeps painting through the truncation tail. tuitext.TruncateText
		// slices raw runes and would chop through escape sequences.
		if leftW >= inner {
			leftContent = ansi.Truncate(leftContent, inner, "…")
			return band.Render(leftContent)
		}
		room := inner - leftW - 1
		rightContent = ansi.Truncate(rightContent, room, "…")
		return band.Render(leftContent + bgOnly.Render(" ") + rightContent)
	}
	gap := inner - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	return band.Render(leftContent + bgOnly.Render(strings.Repeat(" ", gap)) + rightContent)
}

// renderChipStatus produces the right-side connection/provider fragment
// of the composer chip strip. Returns "" when there is no connection
// context to surface. All inner spans declare Background(SurfaceSecondary)
// so the parent chip-strip band's bg paints through without ANSI-reset
// gaps between fragments.
func renderChipStatus(ctx composerContext, th tuitheme.Theme) string {
	if ctx.HubAddr == "" && !ctx.Connected && ctx.Provider == "" {
		return ""
	}
	bg := th.SurfaceSecondary
	dim := lipgloss.NewStyle().Background(bg).Foreground(th.TextDim)
	ghost := lipgloss.NewStyle().Background(bg).Foreground(th.TextGhost)
	bgOnly := lipgloss.NewStyle().Background(bg)

	var fragments []string
	healthClr := th.StateAwaiting
	healthLabel := "disconnected"
	if ctx.Connected {
		healthClr = th.StateIdle
		healthLabel = "connected"
	}
	health := lipgloss.NewStyle().Background(bg).Foreground(healthClr).Bold(true).Render("●") +
		bgOnly.Render(" ") + dim.Render(healthLabel)
	fragments = append(fragments, health)
	if ctx.HubAddr != "" {
		fragments = append(fragments, dim.Render(ctx.HubAddr))
	}
	sep := ghost.Render(" · ")
	return strings.Join(fragments, sep)
}

// composeProviderModel returns "<provider>/<abbreviated-model>" when a
// provider is known, or just the abbreviated model otherwise.
//
// modeldisplay.AbbreviateModel strips the first slash-segment of model (the
// instance name), so we abbreviate first and then strip only a *duplicate*
// outer provider prefix from the result. That keeps two cases right:
//   - Standard instance ("openai/gpt-5" via provider="openai"):
//     AbbreviateModel strips "openai/" → "gpt-5"; no duplicate left to trim;
//     we return "openai/gpt-5".
//   - Nested routing ("openrouter/anthropic/claude-opus-4" via
//     provider="openrouter"): AbbreviateModel strips "openrouter/" →
//     "anthropic/claude-opus-4"; no duplicate outer "openrouter/" left;
//     we return "openrouter/anthropic/claude-opus-4".
func composeProviderModel(provider, model string) string {
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	abbr := modeldisplay.AbbreviateModel(model)
	if provider != "" {
		abbr = strings.TrimPrefix(abbr, provider+"/")
	}
	if provider == "" {
		return abbr
	}
	if abbr == "" {
		return provider
	}
	return provider + "/" + abbr
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
			KbdHint("⌘P", "palette"),
			KbdHint("esc", "browse"),
			KbdHint("/help", ""),
		)
	}
}
