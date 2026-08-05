package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/modeldisplay"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
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
	// Retry is the pending model-call retry (hubModel.modelRetry), rendered as
	// a status fragment so a rate-limited session reads as waiting rather than
	// wedged. Empty when nothing is being retried.
	Retry string
}

// renderComposerChipStrip renders the live-context band above the composer
// textarea. Left: harness/model/branch/dir chips separated by ·. Right:
// connection status and, when in queue/fork/awaiting mode, a state-colored
// mode chip. The whole line is painted as a solid SurfaceSecondary band.
//
// Degradation order (kata wqyx): the working-dir path is static context the
// user already knows, so it is the first thing to shrink — via
// AbbreviatePath's own budget parameter — and the first thing dropped
// outright once shrinking no longer helps. The right side is live state (is
// the hub reachable, is a model call being retried, what mode is the
// composer in) and matters more at narrow widths, so it keeps its full size
// until the dir has already been dropped and the row still doesn't fit;
// only then does it degrade, in priority order: connection health, then the
// retry chip (kata e79v: it exists specifically to explain a hung-looking
// session), then the mode chip, then the hub address — static context
// repeated every render, and the first thing dropped. See renderChipStatus
// and fitRightContent for the mechanics.
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
	sep := ghost.Render(" · ")
	sepW := lipgloss.Width(sep)

	// Left side: harness/model/branch. The working directory is handled
	// separately below, once the right side's ideal size is known, so it
	// can be given whatever budget is left rather than a flat cap.
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
	leftFixed := strings.Join(leftParts, sep)

	// Right side: status + optional mode chip, built at full size — this is
	// what the working-dir budget below is computed against, and what
	// renders whenever it fits.
	rightParts := []string{}
	if status := renderChipStatus(ctx, th, 0); status != "" {
		rightParts = append(rightParts, status)
	}
	modeFrag := ""
	if ctx.Mode != "" {
		modeColor := th.Accent
		switch {
		case strings.HasPrefix(ctx.Mode, "QUEUE"):
			modeColor = th.StateWorking
		case strings.HasPrefix(ctx.Mode, "FORK"):
			modeColor = th.StateWarning
		case strings.HasPrefix(ctx.Mode, "AWAITING"):
			modeColor = th.StateAwaiting
		}
		// Build a mode chip inline with the band bg (instead of using
		// the shared tuiprim.StatusBadge, which has no band bg).
		modeFrag = lipgloss.NewStyle().Background(bg).Foreground(modeColor).Bold(true).Render("● " + strings.ToUpper(ctx.Mode))
		rightParts = append(rightParts, modeFrag)
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

	inner := max(width-2, 1)
	rightW := lipgloss.Width(rightContent)

	// Working-dir chip: shrink into whatever room is left after the fixed
	// left chips and the full right side, capped at composerWorkingDirMaxWidth
	// (a display ceiling, not just a narrow-width fallback — the chip never
	// grew past this even before kata wqyx). Below composerWorkingDirMinWidth
	// the abbreviation is too mangled to be worth showing, so the chip is
	// dropped rather than rendered as a near-empty "…" fragment.
	leftContent := leftFixed
	if ctx.WorkingDir != "" {
		fixedW := lipgloss.Width(leftFixed)
		dirSepW := 0
		if fixedW > 0 {
			dirSepW = sepW
		}
		budget := min(inner-fixedW-dirSepW-rightW-1, composerWorkingDirMaxWidth)
		if budget >= composerWorkingDirMinWidth {
			dirChip := dim.Render(modeldisplay.AbbreviatePath(ctx.WorkingDir, budget))
			if fixedW > 0 {
				leftContent = leftFixed + sep + dirChip
			} else {
				leftContent = dirChip
			}
		}
	}

	leftW := lipgloss.Width(leftContent)
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
		rightContent = fitRightContent(ctx, th, modeFrag, room)
		return band.Render(leftContent + bgOnly.Render(" ") + rightContent)
	}
	gap := inner - leftW - rightW
	return band.Render(leftContent + bgOnly.Render(strings.Repeat(" ", gap)) + rightContent)
}

// composerWorkingDirMaxWidth caps the working-dir chip's AbbreviatePath
// budget even when the row has room to spare; composerWorkingDirMinWidth is
// the floor below which the abbreviation is too mangled to be worth
// showing, so the chip is dropped instead of rendering a near-empty "…".
const (
	composerWorkingDirMaxWidth = 32
	composerWorkingDirMinWidth = 12
)

// fitRightContent rebuilds the chip strip's right side within room columns
// for the case where even a working-dir-less left side leaves no space for
// the full right side (kata wqyx). The mode chip is short and must survive
// whenever the row is wide enough to show anything at all, so its full
// width is reserved first; renderChipStatus shrinks connection health and
// the retry chip into whatever remains. If even the mode chip alone doesn't
// fit, this falls back to a blind tail-truncate of the full content — the
// pre-fix behavior, kept only for widths too narrow for prioritization to
// matter.
func fitRightContent(ctx composerContext, th tuitheme.Theme, modeFrag string, room int) string {
	bg := th.SurfaceSecondary
	gap := lipgloss.NewStyle().Background(bg).Render("  ")
	gapW := lipgloss.Width(gap)

	fullStatus := renderChipStatus(ctx, th, 0)
	modeW := lipgloss.Width(modeFrag)
	reservedGap := 0
	if fullStatus != "" && modeFrag != "" {
		reservedGap = gapW
	}
	if statusBudget := room - modeW - reservedGap; statusBudget > 0 {
		status := renderChipStatus(ctx, th, statusBudget)
		if status != "" && modeFrag != "" {
			return status + gap + modeFrag
		}
		if status != "" {
			return status
		}
		if modeFrag != "" {
			return modeFrag
		}
	}
	if modeFrag != "" && room >= modeW {
		return modeFrag
	}
	full := modeFrag
	if fullStatus != "" {
		full = fullStatus
		if modeFrag != "" {
			full += gap + modeFrag
		}
	}
	return ansi.Truncate(full, room, "…")
}

// renderChipStatus produces the right-side connection/retry/hub-address
// fragment of the composer chip strip, in priority order: connection
// health, then the pending model-call retry, then the hub address. Returns
// "" when there is no connection context to surface. All inner spans
// declare Background(SurfaceSecondary) so the parent chip-strip band's bg
// paints through without ANSI-reset gaps between fragments.
//
// budget caps the rendered width; <= 0 means unlimited, which is what the
// chip strip uses once it already knows the full-size content fits. When
// budget is positive and tight (kata wqyx): the hub address is dropped
// first — it's static context repeated every render — then the retry chip
// is truncated with an ellipsis so its cause ("rate limited") stays legible
// even when the attempt count and wait don't fit; connection health is
// truncated only as an absolute last resort, since callers generally leave
// it room.
func renderChipStatus(ctx composerContext, th tuitheme.Theme, budget int) string {
	if ctx.HubAddr == "" && !ctx.Connected && ctx.Provider == "" {
		return ""
	}
	bg := th.SurfaceSecondary
	dim := lipgloss.NewStyle().Background(bg).Foreground(th.TextDim)
	ghost := lipgloss.NewStyle().Background(bg).Foreground(th.TextGhost)
	bgOnly := lipgloss.NewStyle().Background(bg)
	sep := ghost.Render(" · ")
	sepW := lipgloss.Width(sep)

	healthClr := th.StateAwaiting
	healthLabel := "disconnected"
	if ctx.Connected {
		healthClr = th.StateIdle
		healthLabel = "connected"
	}
	health := lipgloss.NewStyle().Background(bg).Foreground(healthClr).Bold(true).Render("●") +
		bgOnly.Render(" ") + dim.Render(healthLabel)

	unlimited := budget <= 0
	if !unlimited && lipgloss.Width(health) > budget {
		return ansi.Truncate(health, budget, "…")
	}
	result := health
	remaining := budget - lipgloss.Width(health)

	// Ahead of the hub address: while a model call is being retried, that is
	// the most actionable thing on the line, and the address is static
	// context. Truncated with an ellipsis rather than dropped outright when
	// tight, so its cause stays legible even when the count/wait don't fit.
	if ctx.Retry != "" {
		retry := lipgloss.NewStyle().Background(bg).Foreground(th.StateWarning).Render(ctx.Retry)
		if unlimited {
			result += sep + retry
		} else if remaining > sepW {
			retryBudget := remaining - sepW
			if lipgloss.Width(retry) > retryBudget {
				retry = ansi.Truncate(retry, retryBudget, "…")
			}
			result += sep + retry
			remaining -= sepW + lipgloss.Width(retry)
		}
	}

	// Hub address: static context repeated every render, so it's the
	// lowest-priority fragment here — the first dropped when budget is
	// tight, and (unlike retry) shown whole or not at all: a partially
	// truncated address isn't useful.
	if ctx.HubAddr != "" {
		addr := dim.Render(ctx.HubAddr)
		if unlimited {
			result += sep + addr
		} else if remaining > sepW+lipgloss.Width(addr) {
			result += sep + addr
		}
	}

	return result
}

// composerRetryChip renders a pending model-call retry for the chip strip:
// cause, position in the retry budget, and the wait. The wait is the
// load-bearing part — it is what separates "back in 60s" from "wedged", which
// is the whole reason the retry is surfaced at all.
//
// Only rate limiting gets its own wording; it is the one a user can act on
// (wait, or switch model) and overwhelmingly the common case. Anything else
// retryable reads as a generic provider error rather than leaking an
// error-class token like "server" into the UI.
func composerRetryChip(retry *appwire.ThreadModelRetryParams) string {
	if retry == nil {
		return ""
	}
	cause := "provider error"
	if retry.ErrorClass == "rate_limit" {
		cause = "rate limited"
	}
	seconds := max((retry.DelayMS+500)/1000, 0)
	return fmt.Sprintf("%s · retry %d/%d · %ds", cause, retry.Attempt, retry.MaxAttempts, seconds)
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
		hints := []string{tuiprim.KbdHint("enter", "queue")}
		if canSteer {
			hints = append(hints, tuiprim.KbdHint("ctrl+s", "steer"))
		}
		hints = append(hints,
			tuiprim.KbdHint("esc", "browse"),
			tuiprim.KbdHint("⌘P", "palette"),
			tuiprim.KbdHint("⌘O", "dashboard"),
		)
		return tuiprim.ActionBarForWidth(width, hints...)
	case "fork":
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("enter", "fork"),
			tuiprim.KbdHint("esc", "cancel"),
			tuiprim.KbdHint("⌘O", "dashboard"),
		)
	case "scroll-browse":
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("↑↓", "select"),
			tuiprim.KbdHint("enter", "expand"),
			tuiprim.KbdHint("f", "fork"),
			tuiprim.KbdHint("c", "copy"),
			tuiprim.KbdHint("esc", "compose"),
			tuiprim.KbdHint("⌘O", "dashboard"),
		)
	default: // compose
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("enter", "send"),
			tuiprim.KbdHint("shift+enter", "newline"),
			tuiprim.KbdHint("⌘P", "palette"),
			tuiprim.KbdHint("esc", "browse"),
			tuiprim.KbdHint("/help", ""),
		)
	}
}
