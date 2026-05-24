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

// renderComposerChipStrip renders the live-context band above the composer
// textarea. Left: harness/model/branch/dir chips separated by ·. Right:
// connection status and, when in queue/fork/awaiting mode, a state-colored
// mode chip. The whole line is painted as a solid SurfaceSecondary band so
// it reads as a distinct accent strip rather than a divider.
func renderComposerChipStrip(ctx composerContext) string {
	th := activeTheme()

	// Left side: harness/model/branch/dir
	leftParts := []string{}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k := lipgloss.NewStyle().Foreground(th.TextDim).Render(key)
		v := lipgloss.NewStyle().Foreground(th.Text).Render(value)
		leftParts = append(leftParts, k+" "+v)
	}
	add("harness", ctx.Harness)
	add("model", abbreviateModel(ctx.Model))
	add("branch", ctx.Branch)
	if ctx.WorkingDir != "" {
		leftParts = append(leftParts, lipgloss.NewStyle().Foreground(th.TextDim).Render(abbreviatePath(ctx.WorkingDir, 32)))
	}
	sep := lipgloss.NewStyle().Foreground(th.TextGhost).Render(" · ")
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
		rightParts = append(rightParts, StatusBadge(modeColor, ctx.Mode))
	}
	rightContent := strings.Join(rightParts, "  ")

	width := ctx.Width
	if width <= 0 {
		width = 80
	}

	band := lipgloss.NewStyle().
		Background(th.SurfaceSecondary).
		Foreground(th.Text).
		Width(width).
		Padding(0, 1)

	// Content fits in width-2 cells after padding.
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	leftW := lipgloss.Width(leftContent)
	rightW := lipgloss.Width(rightContent)
	if leftW+rightW+1 > inner {
		// Too tight: prioritize left content, drop or truncate the right.
		if leftW >= inner {
			leftContent = truncateText(leftContent, inner)
			return band.Render(leftContent)
		}
		// Show left + as much right as fits.
		room := inner - leftW - 1
		rightContent = truncateText(rightContent, room)
		return band.Render(leftContent + " " + rightContent)
	}
	gap := inner - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	return band.Render(leftContent + strings.Repeat(" ", gap) + rightContent)
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
	sep := lipgloss.NewStyle().Foreground(th.TextGhost).Render(" · ")
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
