package main

import "strings"

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
	Attachments []*PastedImage
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
	keys := []string{"esc: browse", "ctrl+p: palette", "ctrl+o: dashboard", hubCommandHint("help")}
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
		panel.Label = "message"
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
	styles := defaultTUIStyles()

	// Chip strip: always show if ChipContext has any content.
	if p.ChipContext.Harness != "" || p.ChipContext.Model != "" || p.ChipContext.Branch != "" {
		strip := renderComposerChipStrip(p.ChipContext)
		b.WriteString(strip)
		b.WriteString("\n")
	}

	if len(p.QueuePreview) > 0 {
		b.WriteString(renderQueuePreview(p.QueuePreview, p.Width, styles))
	}
	label := strings.TrimSpace(p.Label)
	reason := strings.TrimSpace(p.ReadOnlyReason)
	if reason != "" {
		if label == "" {
			label = "read-only"
		}
		b.WriteString(styles.Error.Render(label + ": " + reason))
		b.WriteString("\n")
	} else if label != "" {
		b.WriteString(styles.Section.Render(label))
		b.WriteString("\n")
	}
	if p.ShowInput {
		b.WriteString(renderComposerDraft(p.Draft, p.MaxDraftLines))
	}
	if len(p.Attachments) > 0 {
		b.WriteString(renderAttachmentChips(p.Attachments, styles))
	}
	// Use mode-aware footer hints when available; fall back to Keys for
	// contexts that do not supply a ChipContext (e.g. tests building
	// composerPanel directly with only Keys set).
	if p.ChipContext.Harness != "" || p.ChipContext.Model != "" || p.ChipContext.Branch != "" {
		footer := composerFooterHints(p.composerModeForFooter(), p.Width, p.CanSteer)
		if footer != "" {
			b.WriteString(styles.Muted.Render(footer))
			b.WriteString("\n")
		}
	} else if len(p.Keys) > 0 {
		b.WriteString(styles.Muted.Render(actionBarForWidth(p.Width, p.Keys...)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderAttachmentChips renders a row of chips for the staged image
// attachments. Each chip is "📎 <name> (WxH) [×]" so the user sees
// what's queued. The header advertises Alt+Backspace as the way to
// drop the most recent chip (kata 5vxd) — the [×] marker is still
// rendered to signal the chip is removable.
func renderAttachmentChips(atts []*PastedImage, styles tuiStyles) string {
	var b strings.Builder
	b.WriteString(styles.Section.Render("attachments"))
	b.WriteString(styles.Muted.Render("  alt+backspace: drop last"))
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
		b.WriteString(styles.Muted.Render("📎 " + name + dims + " [×]"))
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
func renderQueuePreview(preview []string, width int, styles tuiStyles) string {
	var b strings.Builder
	header := "queued"
	if n := len(preview); n > 0 {
		header = "queued (" + itoa(n) + ")"
	}
	b.WriteString(styles.Section.Render(header))
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
		b.WriteString(styles.Muted.Render(line))
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

func renderComposerDraft(draft string, maxLines ...int) string {
	var b strings.Builder
	lines := strings.Split(draft, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	limit := 0
	if len(maxLines) > 0 {
		limit = maxLines[0]
	}
	if limit > 0 && len(lines) > limit {
		if limit == 1 {
			lines = []string{"..."}
		} else {
			lines = append([]string{"..."}, lines[len(lines)-(limit-1):]...)
		}
	}
	for i, line := range lines {
		if i == 0 {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		if i == len(lines)-1 {
			line += "█"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
