package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
)

type composerPanel struct {
	Label          string
	ReadOnlyReason string
	Draft          string
	MaxDraftLines  int
	Keys           []string
	ShowInput      bool
	Width          int
	// CanSteer indicates the session supports Ctrl+S force-steer. Used by
	// composerFooterHints to conditionally include the steer hint.
	CanSteer bool
	// QueuePreview is the list of queued user messages (first line truncated,
	// head-first) rendered above the composer when depth > 0. Set by
	// sessionComposerPanel from the model's local queue.
	QueuePreview []string
	// Attachments are the pending image attachments shown as chips
	// between the composer textarea and the queue preview. Each chip
	// renders as "📎 <name> (WxH) [×]".
	Attachments []*clipboard.PastedImage
	// ChipContext provides metadata for the chip strip rendered above the textarea.
	ChipContext composerContext
}

type hubComposerMode int

const (
	hubComposerModeSend hubComposerMode = iota
	// hubComposerModeQueue replaces the old "steer" auto-switch: while a
	// turn is in flight, Enter enqueues the composer text via turn/queue
	// (kata 111a). Ctrl+S drains the queue as a single STEERING message
	// (kata 0bq1). The composer no longer auto-routes Enter to steer.
	hubComposerModeQueue
	hubComposerModeFork
	hubComposerModeReadOnly
)

func (m hubModel) sessionComposerMode() hubComposerMode {
	if m.forkDraft != nil {
		return hubComposerModeFork
	}
	if m.sessionTurnActionState() {
		if m.detail.Capabilities.Queue {
			return hubComposerModeQueue
		}
		if m.detail.Capabilities.Send {
			return hubComposerModeSend
		}
		return hubComposerModeReadOnly
	}
	if m.sessionCanStartTurn() {
		return hubComposerModeSend
	}
	return hubComposerModeReadOnly
}

func (m hubModel) sessionComposerReadOnlyReason() string {
	if m.sessionTurnActionState() {
		if !m.detail.Capabilities.Queue {
			if m.detail.Capabilities.Send {
				return ""
			}
			return "source does not advertise queue"
		}
	}
	if !m.sessionCanStartTurn() {
		return "source does not support send"
	}
	return ""
}

func (m hubModel) sessionCanStartTurn() bool {
	if m.detail.Capabilities.Send {
		return true
	}
	return !m.detail.Live && m.detail.Capabilities.Resume
}

func (m hubModel) sessionTurnActionState() bool {
	switch stateLabel(m.detail.State) {
	case "active", "awaiting":
		return true
	}
	return m.session.processing
}

func (m hubModel) sessionComposerPanel() composerPanel {
	// "/help" is inlined rather than routed through hubCommandHint("help")
	// because syncSessionViewport (called from enterSessionBrowse) reaches
	// this function via sessionChromeText; routing through hubCommandHint
	// would close an init cycle hubCommandRegistry → enterSessionBrowse →
	// syncSessionViewport → … → hubCommandByName → hubCommandRegistry.
	keys := []string{"esc: browse", "ctrl+p: palette", "ctrl+o: dashboard", "/help"}
	panel := composerPanel{
		Draft:         m.session.input.Value(),
		MaxDraftLines: m.session.input.MaxHeight,
		Keys:          keys,
		ShowInput:     true,
		Width:         m.width,
		QueuePreview:  m.sessionQueuePreview(),
		Attachments:   m.pendingAttachments,
		ChipContext: composerContext{
			Harness:    m.detail.SourceLabel,
			Model:      m.detail.Model,
			Branch:     m.detail.Branch,
			WorkingDir: m.detail.WorkingDir,
			Connected:  m.client != nil,
			HubAddr:    m.hubURL,
			Provider:   firstNonEmptyString(m.detail.Profile, providerFromModel(m.detail.Model)),
			Width:      m.width,
		},
	}
	switch m.sessionComposerMode() {
	case hubComposerModeFork:
		panel.Label = "fork draft"
		panel.Keys = []string{"enter: fork", "esc: cancel", "ctrl+o: dashboard"}
		panel.ChipContext.Mode = "FORK DRAFT"
		// Fork mode shouldn't surface the live-session queue.
		panel.QueuePreview = nil
	case hubComposerModeQueue:
		panel.Label = "queue"
		queueHints := []string{"enter: queue"}
		// Only advertise force-steer when the source also has steer; some
		// sources may someday advertise queue without steer.
		if m.detail.Capabilities.Steer {
			queueHints = append(queueHints, "ctrl+s: send as steer")
			panel.CanSteer = true
		}
		panel.Keys = append(queueHints, keys...)
		queueDepth := len(m.sessionQueue)
		if queueDepth > 0 {
			panel.ChipContext.Mode = "QUEUE " + itoa(queueDepth)
		} else {
			panel.ChipContext.Mode = "QUEUE"
		}
	case hubComposerModeReadOnly:
		panel.Label = "read-only"
		panel.ReadOnlyReason = m.sessionComposerReadOnlyReason()
	default:
		// No section label in default compose mode — the chip strip
		// already carries all the live context; an extra "message" line
		// is redundant chrome.
		if m.sessionCanStartTurn() {
			panel.Keys = append([]string{"enter: send"}, keys...)
		}
	}
	return panel
}

// sessionQueuePreview returns the wire-sourced queue snapshot
// (head-first) for the current session. The TUI no longer mirrors local
// enqueues; entries are populated from thread.Serf.Queue on ReadThread
// and from thread/queueChanged notifications (kata r80p). Each entry has
// already been collapsed to its first line by the daemon.
func (m hubModel) sessionQueuePreview() []string {
	if len(m.sessionQueue) == 0 {
		return nil
	}
	out := make([]string, len(m.sessionQueue))
	copy(out, m.sessionQueue)
	return out
}

// composerModeForFooter maps the composer label/mode chip to the string key
// used by composerFooterHints.
func (p composerPanel) composerModeForFooter() string {
	switch p.Label {
	case "queue":
		return "queue"
	case "fork draft":
		return "fork"
	}
	// Default compose mode.
	return "compose"
}

func (p composerPanel) View() string {
	var b strings.Builder
	th := activeTheme()
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mutedStyle := lipgloss.NewStyle().Foreground(th.TextDim)
	errorStyle := lipgloss.NewStyle().Foreground(th.StateAwaiting).Bold(true)

	// Chip strip: always show if ChipContext has any content.
	if p.ChipContext.Harness != "" || p.ChipContext.Model != "" || p.ChipContext.Branch != "" {
		strip := renderComposerChipStrip(p.ChipContext)
		b.WriteString(strip)
		b.WriteString("\n")
	}

	if len(p.QueuePreview) > 0 {
		b.WriteString(renderQueuePreview(p.QueuePreview, p.Width))
	}
	label := strings.TrimSpace(p.Label)
	reason := strings.TrimSpace(p.ReadOnlyReason)
	if reason != "" {
		if label == "" {
			label = "read-only"
		}
		b.WriteString(errorStyle.Render(label + ": " + reason))
		b.WriteString("\n")
	} else if label != "" {
		b.WriteString(sectionStyle.Render(label))
		b.WriteString("\n")
	}
	if p.ShowInput {
		b.WriteString(renderComposerDraft(p.Draft, p.Width, p.MaxDraftLines))
	}
	if len(p.Attachments) > 0 {
		b.WriteString(renderAttachmentChips(p.Attachments))
	}
	// Use mode-aware footer hints when available; fall back to Keys for
	// contexts that do not supply a ChipContext (e.g. tests building
	// composerPanel directly with only Keys set).
	if p.ChipContext.Harness != "" || p.ChipContext.Model != "" || p.ChipContext.Branch != "" {
		footer := composerFooterHints(p.composerModeForFooter(), p.Width, p.CanSteer)
		if footer != "" {
			b.WriteString(mutedStyle.Render(footer))
			b.WriteString("\n")
		}
	} else if len(p.Keys) > 0 {
		b.WriteString(mutedStyle.Render(actionBarForWidth(p.Width, p.Keys...)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderAttachmentChips renders a row of chips for the staged image
// attachments. Each chip is "📎 <name> (WxH) [×]" so the user sees
// what's queued. The header advertises Alt+Backspace as the way to
// drop the most recent chip (kata 5vxd) — the [×] marker is still
// rendered to signal the chip is removable.
func renderAttachmentChips(atts []*clipboard.PastedImage) string {
	th := activeTheme()
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mutedStyle := lipgloss.NewStyle().Foreground(th.TextDim)
	var b strings.Builder
	b.WriteString(sectionStyle.Render("attachments"))
	b.WriteString(mutedStyle.Render("  alt+backspace: drop last"))
	b.WriteString("\n")
	for _, att := range atts {
		if att == nil {
			continue
		}
		name := filepathBase(att.Path)
		dims := ""
		if att.Width > 0 && att.Height > 0 {
			dims = " (" + itoa(att.Width) + "x" + itoa(att.Height) + ")"
		}
		b.WriteString(mutedStyle.Render("📎 " + name + dims + " [×]"))
		b.WriteString("\n")
	}
	return b.String()
}

// filepathBase returns the last path element of p without dragging in
// filepath here in composer_panel.go. We keep it local so the chip
// renderer stays a pure-function leaf.
func filepathBase(p string) string {
	if p == "" {
		return ""
	}
	idx := strings.LastIndexAny(p, `/\`)
	if idx < 0 {
		return p
	}
	return p[idx+1:]
}

// renderQueuePreview formats the locally tracked queue above the composer.
// Each entry is shown as `[N] first-line` with the first line truncated to
// roughly the composer width. Returns the lines including the section header
// and a trailing newline.
func renderQueuePreview(preview []string, width int) string {
	th := activeTheme()
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mutedStyle := lipgloss.NewStyle().Foreground(th.TextDim)
	var b strings.Builder
	header := "queued"
	if n := len(preview); n > 0 {
		header = "queued (" + itoa(n) + ")"
	}
	b.WriteString(sectionStyle.Render(header))
	b.WriteString("\n")
	maxLine := width - 6
	if maxLine < 20 {
		maxLine = 20
	}
	for i, entry := range preview {
		first := strings.TrimRight(strings.SplitN(entry, "\n", 2)[0], "\r")
		if runes := []rune(first); len(runes) > maxLine {
			first = string(runes[:maxLine-1]) + "…"
		}
		line := "  " + itoa(i+1) + ". " + first
		b.WriteString(mutedStyle.Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

// itoa is a tiny local int-to-string for the queue preview to avoid pulling
// strconv just for this hot UI path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// renderComposerDraft renders the user's in-progress message. Long logical
// lines are soft-wrapped to the available column width so the composer
// reflects what will actually be sent and grows as the user types. The cursor
// glyph (█) is placed at the end of the last visual row.
//
// width is the total column budget for the composer (including the 2-column
// "> " / "  " prefix). maxLines caps the number of visual rows; when the
// content exceeds the cap, an ellipsis row is shown at the top and the most
// recent rows are kept at the bottom. width <= 2 disables soft-wrap (used by
// tests that don't care about wrap geometry).
func renderComposerDraft(draft string, width, maxLines int) string {
	// Reserve the 2-column gutter ("> " on the first row, "  " on the rest).
	inner := width - 2
	logical := strings.Split(draft, "\n")
	if len(logical) == 0 {
		logical = []string{""}
	}

	var rows []string
	for _, line := range logical {
		if inner > 0 && uniWidth(line) > inner {
			wrapped := ansi.Hardwrap(ansi.Wordwrap(line, inner, ""), inner, true)
			rows = append(rows, strings.Split(wrapped, "\n")...)
		} else {
			rows = append(rows, line)
		}
	}
	if len(rows) == 0 {
		rows = []string{""}
	}

	if maxLines > 0 && len(rows) > maxLines {
		if maxLines == 1 {
			rows = []string{"..."}
		} else {
			rows = append([]string{"..."}, rows[len(rows)-(maxLines-1):]...)
		}
	}

	var b strings.Builder
	for i, text := range rows {
		if i == 0 {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		if i == len(rows)-1 {
			text += "█"
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

// uniWidth measures a string's display width. We rely on ansi's Wordwrap/
// Hardwrap which already account for grapheme clusters internally; the only
// reason to measure here is to skip the wrap call for short lines.
func uniWidth(s string) int {
	return ansi.StringWidth(s)
}
