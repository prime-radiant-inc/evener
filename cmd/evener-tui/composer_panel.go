package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
	"primeradiant.com/evener/envvars"
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
	// AwaitingQuestion shows the "question waiting" chip whenever a question
	// is genuinely pending — an unresolved ask_user call in the transcript,
	// per pendingAskQuestions (spec §6.2) — independent of ChipContext,
	// which carries harness/model/branch metadata rather than transient
	// state. This is NOT simply "the session is awaiting": under
	// attention-status-model v5 a session re-arms State=="awaiting" after
	// any clean output-producing turn, including the reply that resolves
	// an ask_user question, so an awaiting rest can have nothing pending.
	AwaitingQuestion bool
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

// sessionControls is the Go twin of the SDK's sessionControls
// (appwire-client/typescript/submitRouting.ts, which keeps the rationale):
// what this session may be asked to do now, from the wire's status, the
// harness's capabilities and the queue depth. Every affordance and key in the
// TUI reads a field of it; none reads a raw capability. The status is the
// wire's alone -- the optimistic processing flag routes Enter (sessionTurnRunning)
// but never a control, and the transcript's turn id never enters.
//
//	stop   active && interrupt  (the hub advertises interrupt as harness support)
//	steer  active && steer      (the hub advertises steer as harness support)
//	drain  steer && (active || idle with a non-empty queue, the one a Stop parked),
//	       and not while the queue revision is stale after a partial drain
//	queue  active && queue      (the hub advertises queue as harness support)
//	send   !active && send      (the hub folds the status into send; the status is
//	                             applied here too, so a source that advertises send
//	                             while a turn runs is not composed into, since
//	                             turn/start would be refused)
type sessionControls struct {
	stop, steer, drain, queue, send bool
	// drainReason says why drain is false: the harness, the status, or the
	// queue revision still syncing after a partial drain. Empty when true.
	drainReason string
}

func (m hubModel) sessionControls() sessionControls {
	active := m.detail.State == appwire.ThreadStatusActive
	parked := m.detail.State == appwire.ThreadStatusIdle && m.detail.Queue.Depth > 0
	caps := m.detail.Capabilities
	c := sessionControls{
		stop:  active && caps.Interrupt,
		steer: active && caps.Steer,
		drain: caps.Steer && (active || parked) && !m.queueRevisionStale,
		queue: active && caps.Queue,
		send:  !active && caps.Send,
	}
	// Ordered like submitRouting.ts's reason.drain: the harness, then the
	// status, then the revision still syncing after a partial drain. The stale
	// flag must not mask an awaiting or idle refusal, which no retry resolves.
	switch {
	case c.drain:
	case !caps.Steer:
		c.drainReason = "source does not advertise steer"
	case !active && !parked:
		c.drainReason = "no active turn"
	default:
		c.drainReason = "the queue is syncing after the last force-steer; retry in a moment"
	}
	return c
}

func (m hubModel) sessionComposerMode() hubComposerMode {
	if m.forkDraft != nil {
		return hubComposerModeFork
	}
	controls := m.sessionControls()
	if m.sessionTurnRunning() {
		if controls.queue {
			return hubComposerModeQueue
		}
		if controls.send {
			return hubComposerModeSend
		}
		return hubComposerModeReadOnly
	}
	if m.sessionCanStartTurn() {
		return hubComposerModeSend
	}
	return hubComposerModeReadOnly
}

// sessionCanDrainQueue: Ctrl+S may drain the queue as steering. It is
// sessionControls' drain, independent of the composer mode (which decides only
// Enter's route) and of the optimistic processing flag: the status alone.
func (m hubModel) sessionCanDrainQueue() bool {
	return m.sessionControls().drain
}

func (m hubModel) sessionComposerReadOnlyReason() string {
	controls := m.sessionControls()
	if m.sessionTurnActionState() {
		if !controls.queue {
			if controls.send {
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
	if m.sessionControls().send {
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

// sessionTurnRunning reports a genuinely in-flight turn (the composer should
// offer Stop/steer/queue). A rested "awaiting" session — re-armed "your move"
// with nothing running — is NOT running: it drops to plain Send. This is
// narrower than sessionTurnActionState (which stays true for awaiting so the
// status line's "busy" affordances and the !processing send-gating are
// unchanged); it governs only the presented composer affordance.
func (m hubModel) sessionTurnRunning() bool {
	if stateLabel(m.detail.State) == "active" {
		return true
	}
	return m.session.processing
}

func (m hubModel) sessionComposerPanel() composerPanel {
	// "/help" is inlined as a literal rather than looked up via
	// hubCommandByName because syncSessionViewport (called from
	// enterSessionBrowse) reaches this function via sessionChromeText; a
	// lookup would close an init cycle hubCommandRegistry → enterSessionBrowse
	// → syncSessionViewport → … → hubCommandByName → hubCommandRegistry.
	keys := []string{"esc: browse", "ctrl+p: palette", "ctrl+o: dashboard", "/help"}
	panel := composerPanel{
		Draft:            m.session.input.Value(),
		MaxDraftLines:    m.session.input.MaxHeight,
		Keys:             keys,
		ShowInput:        true,
		Width:            m.width,
		QueuePreview:     m.sessionQueuePreview(),
		Attachments:      m.pendingAttachments,
		AwaitingQuestion: len(pendingAskQuestions(m.session.messages)) > 0,
		ChipContext: composerContext{
			Harness:    m.detail.SourceLabel,
			Model:      m.detail.Model,
			Branch:     m.detail.Branch,
			WorkingDir: m.detail.WorkingDir,
			Connected:  m.hubConnected(),
			HubAddr:    m.hubURL,
			Provider:   envvars.FirstNonEmpty(m.detail.Profile, providerFromModel(m.detail.Model)),
			Width:      m.width,
			Retry:      composerRetryChip(m.modelRetry, m.detail.Model, m.modelRetryInProgress),
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
		// Only advertise force-steer when it would be accepted: some sources
		// may someday advertise queue without steer.
		if m.sessionCanDrainQueue() {
			queueHints = append(queueHints, "ctrl+s: send as steer")
			panel.CanSteer = true
		}
		queueHints = append(queueHints, keys...)
		panel.Keys = queueHints
		// The wire's depth, not the preview's length: the preview only renders
		// rows and may lag or be absent.
		queueDepth := m.detail.Queue.Depth
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
			sendKeys := []string{"enter: send"}
			// A queue a Stop parked: Ctrl+S runs it as steering now.
			if m.sessionCanDrainQueue() {
				sendKeys = append(sendKeys, "ctrl+s: run queue as steer")
				panel.CanSteer = true
			}
			sendKeys = append(sendKeys, keys...)
			panel.Keys = sendKeys
		}
		if depth := m.detail.Queue.Depth; depth > 0 {
			panel.ChipContext.Mode = "QUEUE " + itoa(depth)
		}
	}
	return panel
}

// sessionQueuePreview returns the wire-sourced queue snapshot
// (head-first) for the current session. The TUI no longer mirrors local
// enqueues; entries are populated from thread.Evener.Queue on ReadThread
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
	th := tuitheme.ActiveTheme()
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mutedStyle := lipgloss.NewStyle().Foreground(th.TextDim)
	errorStyle := lipgloss.NewStyle().Foreground(th.StateAwaiting).Bold(true)

	// Chip strip: always show if ChipContext has any content.
	if p.ChipContext.Harness != "" || p.ChipContext.Model != "" || p.ChipContext.Branch != "" {
		strip := renderComposerChipStrip(p.ChipContext)
		b.WriteString(strip)
		b.WriteString("\n")
	}

	// Waiting chip (spec §6.2): shown whenever a question is genuinely
	// pending (an unresolved ask_user call in the transcript), independent
	// of the harness/model chip strip above — NOT merely whenever the
	// session rests awaiting, since attention-status-model v5 can re-arm
	// that rest state with nothing left pending. ctrl+q is the ONLY way to
	// open the question overlay — this chip is discoverability chrome, not
	// a button.
	if p.AwaitingQuestion {
		waitingStyle := lipgloss.NewStyle().Foreground(th.StateAwaiting).Bold(true)
		b.WriteString(waitingStyle.Render("◆ question waiting — ctrl+q to answer"))
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
		b.WriteString(mutedStyle.Render(tuiprim.ActionBarForWidth(p.Width, p.Keys...)))
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
	th := tuitheme.ActiveTheme()
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
	th := tuitheme.ActiveTheme()
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mutedStyle := lipgloss.NewStyle().Foreground(th.TextDim)
	var b strings.Builder
	header := "queued"
	if n := len(preview); n > 0 {
		header = "queued (" + itoa(n) + ")"
	}
	b.WriteString(sectionStyle.Render(header))
	b.WriteString("\n")
	maxLine := max(width-6, 20)
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

	var rows []string
	for _, line := range logical {
		if inner > 0 && uniWidth(line) > inner {
			wrapped := ansi.Hardwrap(ansi.Wordwrap(line, inner, ""), inner, true)
			rows = append(rows, strings.Split(wrapped, "\n")...)
		} else {
			rows = append(rows, line)
		}
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
