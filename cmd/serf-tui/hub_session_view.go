package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/modeldisplay"
	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func (m hubModel) sessionHeaderLines() []string {
	th := tuitheme.ActiveTheme()
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	state := strings.TrimSpace(m.detail.State)
	if state == "" {
		state = "idle"
	}

	// Line 1: section divider rule with breadcrumb + turn count
	rule := tuiprim.SectionDivider(m.sessionHeaderWidth(), "SERF / SESSION", fmt.Sprintf("%d turns", m.detail.TurnCount))

	// Line 2: title + state badge (truncate title if needed to fit width)
	// Use stateLabel to normalize raw states (e.g. "closed" → "ended").
	normalizedState := stateLabel(state)
	badge := tuiprim.StatusBadge(stateColor(normalizedState), normalizedState)
	badgeW := lipgloss.Width(badge)
	maxTitleW := m.sessionHeaderWidth() - 2 - 3 - badgeW // 2-space indent + 3-space gap
	if maxTitleW < 4 {
		maxTitleW = 4
	}
	displayTitle := title
	if lipgloss.Width(displayTitle) > maxTitleW {
		displayTitle = truncateSessionLine(displayTitle, maxTitleW)
	}
	titleLine := "  " + lipgloss.NewStyle().Bold(true).Foreground(th.Text).Render(displayTitle) + "   " + badge

	// Line 3: meta strip — key/value pairs separated by ·
	var parts []string
	addPart := func(key, value string) {
		if value == "" {
			return
		}
		k := lipgloss.NewStyle().Foreground(th.TextDim).Render(key)
		v := lipgloss.NewStyle().Foreground(th.Text).Render(value)
		parts = append(parts, k+" "+v)
	}
	addPart("src", firstNonEmptyString(m.detail.SourceLabel, sourceLabelFromRefText(m.detail.Ref)))
	addPart("branch", m.detail.Branch)
	addPart("model", modeldisplay.AbbreviateModel(m.detail.Model))
	if m.detail.WorkingDir != "" {
		addPart("dir", modeldisplay.AbbreviatePath(m.detail.WorkingDir, 32))
	}
	if ctx := formatContextFragment(m.detail); ctx != "" {
		addPart("ctx", ctx)
	}
	if m.detail.Goal != nil {
		addPart("goal", fmt.Sprintf("%s %d", m.detail.Goal.Status, m.detail.Goal.Iterations))
	}
	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	meta := "  " + strings.Join(parts, sep)
	// Truncate meta line to header width to prevent overflow
	if lipgloss.Width(meta) > m.sessionHeaderWidth() {
		meta = truncateSessionLine(meta, m.sessionHeaderWidth())
	}

	return []string{rule, titleLine, meta}
}

func (m hubModel) sessionHeaderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 100
}

func (m hubModel) sessionStatusLine() string {
	parts := []string{"status: " + m.hubConnectionLabel()}
	if readiness := m.sessionAuthReadinessLabel(); readiness != "" {
		parts = append(parts, readiness)
	}
	parts = append(parts, m.sessionCapabilityStatusLabel())
	if m.sessionTurnActionState() {
		busy := "busy"
		if turnID := strings.TrimSpace(m.detail.ActiveTurnID); turnID != "" {
			busy += ": " + turnID
		}
		parts = append(parts, busy)
	}
	if errText := m.sessionStatusErrorText(); errText != "" {
		parts = append(parts, "error: "+errText)
	}
	return strings.Join(parts, "  ")
}

func (m hubModel) hubConnectionLabel() string {
	if m.client == nil {
		return "hub disconnected"
	}
	return "hub connected"
}

func (m hubModel) sessionAuthReadinessLabel() string {
	if m.authStatusSeen {
		provider := firstNonEmptyString(m.authStatus.Provider, "provider")
		source := strings.TrimSpace(m.authStatus.ActiveSource)
		switch source {
		case "":
			source = "unknown"
		case "signed-out":
			source = "signed out"
		}
		return "auth: " + provider + " " + source
	}
	if provider := strings.TrimSpace(m.detail.Profile); provider != "" {
		return "provider: " + provider
	}
	if provider, _, ok := strings.Cut(strings.TrimSpace(m.detail.Model), "/"); ok && strings.TrimSpace(provider) != "" {
		return "provider: " + provider
	}
	return "auth: unknown"
}

func (m hubModel) sessionCapabilityStatusLabel() string {
	switch m.sessionComposerMode() {
	case hubComposerModeQueue:
		return "queue: ready"
	case hubComposerModeReadOnly:
		reason := m.sessionComposerReadOnlyReason()
		if reason == "" {
			reason = "send is not available"
		}
		return "read-only: " + reason
	case hubComposerModeFork:
		return "fork: draft"
	default:
		return "send: ready"
	}
}

// forkDraftHeader returns a tuiprim.SectionDivider for the fork-draft UI surface,
// showing the branch name and diverge-turn info as the right label.
func forkDraftHeader(branch string, divergeTurn int, width int) string {
	right := fmt.Sprintf("%s@diverge:%d", branch, divergeTurn)
	return tuiprim.SectionDivider(width, "fork draft", right)
}

// providerFromModel extracts the provider prefix from "provider/model" strings.
func providerFromModel(model string) string {
	if provider, _, ok := strings.Cut(strings.TrimSpace(model), "/"); ok {
		return strings.TrimSpace(provider)
	}
	return ""
}

func (m hubModel) sessionStatusErrorText() string {
	if m.err != nil {
		return m.err.Error()
	}
	return strings.TrimSpace(m.sessionStatusError)
}

// renderSessionMainBody returns the scrollable body content for the session view:
// header lines, status, errors, notices, fork draft header, and message list.
func (m hubModel) renderSessionMainBody() string {
	var b strings.Builder
	for _, line := range m.sessionHeaderLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if statusLine := m.sessionStatusLine(); statusLine != "" {
		b.WriteString(statusLine)
		b.WriteString("\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if notices := m.renderNotices(); notices != "" {
		b.WriteString("\n")
		b.WriteString(notices)
	}
	if m.forkDraft != nil {
		branch := firstNonEmptyString(m.detail.Branch, "fork")
		b.WriteString("\n")
		b.WriteString(forkDraftHeader(branch, m.forkDraft.Turn, m.sessionHeaderWidth()))
		b.WriteString("\n")
	}
	messages := m.session.messages
	if m.transcriptView != nil {
		b.WriteString("\n")
		b.WriteString(tuitheme.SystemStyle.Width(max(m.width, 80)).Render(m.transcriptView.banner()))
		b.WriteString("\n")
		messages = m.transcriptView.Messages
	}
	if len(messages) == 0 {
		b.WriteString("\nNo transcript events yet.\n")
	} else {
		width := m.width
		if width == 0 {
			width = 100
		}
		prevRendered := false
		for i := 0; i < len(messages); i++ {
			msg := messages[i]
			// Consolidate a contiguous run of subagent / background-job entries
			// into one calm delegation rail (the TUI analog of the web rail).
			if isSubagentRunMessage(msg) {
				runs := make([]transcript.SubagentRunInfo, 0, 4)
				selected := false
				j := i
				for j < len(messages) && isSubagentRunMessage(messages[j]) {
					runs = append(runs, *messages[j].Tool.Subagent)
					if m.transcriptView == nil && m.session.scrollMode && m.browseSelected == j {
						selected = true
					}
					j++
				}
				rendered := msgrender.RenderSubagentRail(runs, width)
				if rendered != "" {
					if selected {
						rendered = msgrender.RenderSelectedMessage(rendered, true)
					}
					b.WriteString("\n")
					b.WriteString(rendered)
					b.WriteString("\n")
					prevRendered = true
				}
				i = j - 1
				continue
			}
			focused := false
			rendered := msgrender.RenderMessage(msg, width, focused)
			if rendered == "" {
				continue
			}
			if m.transcriptView == nil && m.session.scrollMode && m.browseSelected == i {
				rendered = msgrender.RenderSelectedMessage(rendered, true)
			}
			if prevRendered && msg.Kind == transcript.MsgUser {
				rule := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().RuleSoft).Render(strings.Repeat("┄", width))
				b.WriteString(rule)
				b.WriteString("\n")
			}
			b.WriteString("\n")
			b.WriteString(rendered)
			b.WriteString("\n")
			prevRendered = true
		}
	}
	return b.String()
}

// isSubagentRunMessage reports whether a message is a subagent / background-job
// run entry (consolidated into the delegation rail rather than rendered as an
// individual tool line).
func isSubagentRunMessage(msg transcript.ChatMessage) bool {
	return msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil
}

// sessionChromeText returns the overlay and footer strings used for body-height
// computation and the tuiprim.AppShell. Extracted so syncSessionViewport and sessionView
// share the same chrome calculation.
func (m *hubModel) sessionChromeText() (topBar, overlayText, footer string) {
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	topBar = truncateSessionLine("serf / session / "+title, m.sessionHeaderWidth())

	// Footer is computed before the overlay so the command palette can window
	// itself to the rows left between the anchored TopBar and Footer (mirrors the
	// dashboard path); the footer never depends on the overlay content.
	switch {
	case m.transcriptView != nil:
		footer = tuiprim.ActionBarForWidth(m.width, "esc/i/q: return to chat", "ctrl+o: dashboard")
	case m.session.scrollMode:
		keys := []string{"esc/i/q: compose", "enter: expand selected", "ctrl+t: expand all"}
		if m.detail.Capabilities.Fork {
			keys = append(keys, "f: fork selected user turn")
		}
		keys = append(keys, "ctrl+o: dashboard")
		footer = tuiprim.ActionBarForWidth(m.width, keys...) + "\n" + m.sessionComposerPanel().View()
	default:
		footer = m.sessionComposerPanel().View()
	}

	var overlay strings.Builder
	if m.questionOverlay != nil && !m.questionOverlay.Deferred() {
		overlay.WriteString(m.questionOverlay.View())
		overlay.WriteString("\n\n")
	}
	if m.sessionModelPicker != nil {
		overlay.WriteString(m.sessionModelPicker.View())
		overlay.WriteString("\n\n")
	}
	if m.sessionThemePicker != nil {
		overlay.WriteString(m.sessionThemePicker.View())
		overlay.WriteString("\n\n")
	}
	if m.sessionTranscriptPicker != nil {
		overlay.WriteString(m.sessionTranscriptPicker.View())
		overlay.WriteString("\n\n")
	}
	if m.sessionPanel != nil {
		overlay.WriteString(m.sessionPanelOverlay())
		overlay.WriteString("\n\n")
	}
	if m.commandPalette != nil {
		overlay.WriteString(m.commandPalette.ViewWithMaxHeight(paletteOverlayHeight(m.height, topBar, "", footer)))
		overlay.WriteString("\n\n")
	}
	if m.launchOverridesModal != nil {
		overlay.WriteString(m.launchOverridesModal.View())
		overlay.WriteString("\n\n")
	}
	if m.followupModal != nil {
		overlay.WriteString(m.followupModal.View())
		overlay.WriteString("\n\n")
	}
	overlayText = overlay.String()
	return topBar, overlayText, footer
}

// syncSessionViewport writes the current mainBody and correct geometry into
// m.session.viewport so that browse-mode scroll handlers (moveBrowsePage,
// updateSessionKey j/k/pgup/pgdown) operate against the same content and
// dimensions the user actually sees. Must be called on an addressable *hubModel
// so mutations persist. Called from Update (session mode) and from
// enterSessionBrowse / exitSessionBrowse.
func (m *hubModel) syncSessionViewport() {
	topBar, overlayText, footer := m.sessionChromeText()
	bodyHeight := sessionShellBodyHeight(m.height, topBar, overlayText, footer)
	if bodyHeight <= 0 {
		return
	}
	mainBody := m.renderSessionMainBody()
	m.session.viewport.Width = max(1, m.width)
	m.session.viewport.Height = bodyHeight
	m.session.viewport.SetContent(strings.TrimRight(mainBody, "\n"))
	if !m.session.scrollMode && m.transcriptView == nil {
		m.session.viewport.GotoBottom()
	}
}

func (m *hubModel) sessionView() string {
	// Sync viewport so the body reflects current state (needed when sessionView
	// is called outside Update, e.g. in tests or via View()).
	m.syncSessionViewport()
	topBar, overlayText, footer := m.sessionChromeText()
	bodyHeight := sessionShellBodyHeight(m.height, topBar, overlayText, footer)
	body := m.sessionBody("", bodyHeight, overlayText != "")
	return tuiprim.AppShell{
		TopBar:  topBar,
		Body:    body,
		Overlay: overlayText,
		Footer:  footer,
		Height:  m.height,
	}.View()
}

func (m hubModel) renderSessionDetails() string {
	return detailsDrawer{Detail: m.detail, HubURL: m.hubURL}.View()
}

// sessionBody is a pure renderer: viewport state is managed by syncSessionViewport.
// The mainBody arg is ignored; bodyHeight guards against the zero-height case
// (e.g. tests that don't set m.height) by falling back to rendering the main
// body directly so content is still visible.
func (m hubModel) sessionBody(_ string, bodyHeight int, _ bool) string {
	if bodyHeight <= 0 {
		return m.renderSessionMainBody()
	}
	return m.session.viewport.View()
}

func (m hubModel) sessionPanelOverlay() string {
	if m.sessionPanel == nil {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	return tuiprim.RenderPopupPane(m.sessionPanel.View(), width)
}
