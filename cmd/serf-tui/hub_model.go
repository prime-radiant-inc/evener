package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/modeldisplay"
	pendingpkg "primeradiant.com/serf/cmd/serf-tui/internal/pending"
	"primeradiant.com/serf/internal/appwire"
)

type hubMode int

const (
	hubModeDashboard hubMode = iota
	hubModeSession
	hubModeSpawn
)

type hubSpawnField int

const (
	hubSpawnFieldPrompt hubSpawnField = iota
	hubSpawnFieldHarness
	hubSpawnFieldModel
	hubSpawnFieldDir
)

type hubRowKind int

const (
	hubRowLaunch hubRowKind = iota
	hubRowProject
	hubRowSession
	hubRowRecentToggle
)

type hubRow struct {
	kind        hubRowKind
	ref         appwire.Ref
	sourceLabel string
	title       string
	project     string
	projectKey  string
	state       string
	live        bool
	model       string
	age         string
	rowID       string
	createdAt   int64
	updatedAt   int64
	liveCount   int
	recentCount int
}

type hubForkDraft struct {
	Ref          appwire.Ref
	Turn         int
	OriginalText string
	Label        string
	Submitting   bool
}

type hubModel struct {
	client   *appwire.Client
	hubURL   string
	stateDir string
	width    int
	height   int
	err      error

	mode     hubMode
	tree     hubTreeResponse
	rows     []hubRow
	selected int

	dashboardFilter        textinput.Model
	dashboardFilterActive  bool
	dashboardRecentOpen    map[string]bool
	dashboardProjectClosed map[string]bool
	dashboardSelectedOnce  bool
	commandPalette         *commandPalette

	browseSelected          int
	forkDraft               *hubForkDraft
	sessionThemePicker      *themePicker
	sessionModelPicker      *modelPicker
	sessionTranscriptPicker *modelPicker
	sessionPanel            *hubSessionPanel
	sessionDetailsRequested bool
	transcriptTargets       []appwire.ThreadTranscriptTarget
	transcriptView          *hubTranscriptViewState
	spawnReturnMode         hubMode
	spawnDir                string
	spawnProject            string
	spawnHarness            string
	spawnHarnesses          []string
	spawnHarnessKinds       map[string]string
	spawnEmptyTaskReasons   map[string]string
	spawnEmptyTaskNext      map[string]string
	spawnModel              string
	spawnModels             []modelPickerItem
	spawnHarnessModels      map[string][]modelPickerItem
	spawnModelPicker        *modelPicker
	spawnDirInput           textinput.Model
	spawnSubmitting         bool
	spawnFocus              hubSpawnField

	detail  hubSessionDetail
	session model
	notices []noticePanel

	authStatus         authStatus
	authStatusSeen     bool
	sessionStatusError string
	statusRefreshToken int

	authLoginProvider string
	authLoginFlowID   string

	credentialsPanel     *credentialsPanel
	launchSettingsPanel  *launchSettingsPanel
	followupModal        *textInputModal
	launchOverridesModal *launchOverridesModal

	spawnLaunchOverrides *appwire.LaunchConfigLayer

	lastCtrlC       time.Time
	postQuitMessage string

	// sessionQueue is the wire-sourced queue preview for the current
	// session — populated from thread.Serf.Queue on ReadThread and from
	// thread/queueChanged notifications (kata r80p). The TUI no longer
	// mirrors local enqueues; it renders straight from this authoritative
	// snapshot, so two clients viewing the same session agree on state.
	// Each entry is a first-line-truncated string in FIFO order.
	// sessionQueueRef scopes the queue to a single session ref so
	// navigating away resets it.
	sessionQueue    []string
	sessionQueueRef string

	// pendingAttachments holds image attachments staged by Ctrl+V or
	// pasted-path detection. Each entry has a backing temp file at
	// PastedImage.Path that the submit flow ships as an InputItem and
	// cleans up afterwards. The slice is rendered as a row of chips
	// below the composer textarea.
	pendingAttachments []*clipboard.PastedImage
	// attachmentSubmitsInFlight counts async submit commands that captured
	// attachment pointers. While non-zero, removed temp files are queued for
	// deferred cleanup so the command can still read them.
	attachmentSubmitsInFlight int
	deferredAttachmentCleanup []*clipboard.PastedImage
	// nextAttachmentMarker is a per-composer high-water counter. Marker
	// numbers are never reused while a composer draft is alive, even if the
	// user removes the highest-numbered attachment.
	nextAttachmentMarker int
	// clipboardSource is the production clipboard reader, swappable in
	// tests via newSessionHubModel + assignment. When nil we lazily
	// install the platform-specific SystemClipboardSource on first use.
	clipboardSource clipboard.ClipboardSource

	// pending coordinates optimistic-rendering placeholders for
	// turn/start, turn/queue, turn/steer, turn/drainAsSteer. Wired
	// from main.go via setSend after tea.NewProgram constructs the
	// program reference.
	pending *pendingpkg.PendingCoordinator
}

const hubCtrlCQuitWindow = time.Second

func newHubModel(client *appwire.Client, hubURL string, stateDirs ...string) hubModel {
	stateDir := ""
	if len(stateDirs) > 0 {
		stateDir = strings.TrimSpace(stateDirs[0])
	}
	session := newModel(nil)
	model := hubModel{client: client, hubURL: hubURL, stateDir: stateDir, session: session, browseSelected: -1, dashboardFilter: newHubFilterInput(), dashboardRecentOpen: map[string]bool{}, dashboardProjectClosed: map[string]bool{}, spawnDirInput: newSpawnDirInput()}
	// Construct the pending coordinator with a buffering placeholder
	// send. main.go calls model.pending.setSend(program.Send) after
	// tea.NewProgram so coordinator-emitted msgs reach Update. Until
	// then, msgs are dropped harmlessly (the coordinator only emits
	// in response to user actions, which can't happen pre-Run).
	model.pending = pendingpkg.NewPendingCoordinator(pendingpkg.RealClock{}, func(tea.Msg) {})
	if client != nil {
		client.SetPendingCoordinator(model.pending)
	}
	return model
}

func newHubFilterInput() textinput.Model {
	input := textinput.New()
	input.Prompt = "filter: "
	input.Placeholder = "title, project, model, source"
	input.CharLimit = 0
	return input
}

func newSpawnDirInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "working directory"
	input.CharLimit = 0
	return input
}

func (m hubModel) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return tea.Batch(fetchHubTree(m.client), waitHubNotification(m.client))
}

func (m hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.updateImpl(msg)
	if hm, ok := next.(hubModel); ok && hm.mode == hubModeSession {
		hm.syncSessionViewport()
		return hm, cmd
	}
	return next, cmd
}

func (m hubModel) View() string {
	if m.mode == hubModeSession {
		return m.sessionView()
	}
	if m.mode == hubModeSpawn {
		return m.spawnView()
	}
	return m.dashboardView()
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func joinDashboardColumns(left, right string, leftWidth, rightWidth, totalWidth int) string {
	leftLines := strings.Split(strings.TrimRight(left, "\n"), "\n")
	rightLines := strings.Split(strings.TrimRight(right, "\n"), "\n")
	lineCount := max(len(leftLines), len(rightLines))
	var b strings.Builder
	for i := 0; i < lineCount; i++ {
		leftLine := ""
		if i < len(leftLines) {
			leftLine = truncateText(leftLines[i], leftWidth)
		}
		rightLine := ""
		if i < len(rightLines) {
			rightLine = truncateText(rightLines[i], rightWidth)
		}
		padding := leftWidth - lipgloss.Width(leftLine)
		if padding < 0 {
			padding = 0
		}
		line := leftLine + strings.Repeat(" ", padding) + "  " + rightLine
		b.WriteString(truncateText(line, totalWidth))
		b.WriteString("\n")
	}
	return b.String()
}

func truncateMultilineText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = truncateText(line, width)
	}
	return strings.Join(lines, "\n")
}

func (m hubModel) sessionHeaderLines() []string {
	th := activeTheme()
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	state := strings.TrimSpace(m.detail.State)
	if state == "" {
		state = "idle"
	}

	// Line 1: section divider rule with breadcrumb + turn count
	rule := SectionDivider(m.sessionHeaderWidth(), "SERF / SESSION", fmt.Sprintf("%d turns", m.detail.TurnCount))

	// Line 2: title + state badge (truncate title if needed to fit width)
	// Use stateLabel to normalize raw states (e.g. "closed" → "ended").
	normalizedState := stateLabel(state)
	badge := StatusBadge(stateColor(normalizedState), normalizedState)
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
	sep := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	meta := "  " + strings.Join(parts, sep)
	// Truncate meta line to header width to prevent overflow
	if lipgloss.Width(meta) > m.sessionHeaderWidth() {
		meta = truncateSessionLine(meta, m.sessionHeaderWidth())
	}

	return []string{rule, titleLine, meta}
}

func sessionHeaderModelSummary(detail hubSessionDetail) string {
	if model := strings.TrimSpace(detail.Model); model != "" {
		return "model: " + model
	}
	if provider := strings.TrimSpace(detail.Profile); provider != "" {
		return "provider: " + provider
	}
	return "model: unknown"
}

func (m hubModel) sessionHeaderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 100
}

func truncateSessionLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
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

// forkDraftHeader returns a SectionDivider for the fork-draft UI surface,
// showing the branch name and diverge-turn info as the right label.
func forkDraftHeader(branch string, divergeTurn int, width int) string {
	right := fmt.Sprintf("%s@diverge:%d", branch, divergeTurn)
	return SectionDivider(width, "fork draft", right)
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
		b.WriteString(systemStyle.Width(max(m.width, 80)).Render(m.transcriptView.banner()))
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
		for i, msg := range messages {
			focused := false
			rendered := renderMessage(msg, width, focused)
			if rendered == "" {
				continue
			}
			if m.transcriptView == nil && m.session.scrollMode && m.browseSelected == i {
				rendered = renderSelectedMessage(rendered, true)
			}
			if prevRendered && msg.Kind == msgUser {
				rule := lipgloss.NewStyle().Foreground(activeTheme().RuleSoft).Render(strings.Repeat("┄", width))
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

// sessionChromeText returns the overlay and footer strings used for body-height
// computation and the appShell. Extracted so syncSessionViewport and sessionView
// share the same chrome calculation.
func (m *hubModel) sessionChromeText() (topBar, overlayText, footer string) {
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	topBar = truncateSessionLine(fmt.Sprintf("serf / session / %s", title), m.sessionHeaderWidth())
	var overlay strings.Builder
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
		overlay.WriteString(m.commandPalette.View())
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
	var kbdFooter string
	switch {
	case m.transcriptView != nil:
		kbdFooter = actionBarForWidth(m.width, "esc/i/q: return to chat", "ctrl+o: dashboard")
	case m.session.scrollMode:
		keys := []string{"esc/i/q: compose", "ctrl+t: expand tools"}
		if m.detail.Capabilities.Fork {
			keys = append(keys, "f: fork selected user turn")
		}
		keys = append(keys, "ctrl+o: dashboard")
		kbdFooter = actionBarForWidth(m.width, keys...) + "\n" + m.sessionComposerPanel().View()
	default:
		kbdFooter = m.sessionComposerPanel().View()
	}
	footer = kbdFooter
	return
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
	return appShell{
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
	return renderPopupPane(m.sessionPanel.View(), width)
}

func sessionShellBodyHeight(totalHeight int, topBar, overlay, footer string) int {
	if totalHeight <= 0 {
		return 0
	}
	fixedLines := 0
	sections := 1
	for _, section := range []string{topBar, overlay, footer} {
		if lines := shellSectionLineCount(section); lines > 0 {
			fixedLines += lines
			sections++
		}
	}
	if sections > 1 {
		fixedLines += 2 * (sections - 1)
	}
	height := totalHeight - fixedLines
	if height < 1 {
		return 1
	}
	return height
}

func shellSectionLineCount(section string) int {
	section = strings.TrimRight(section, "\n")
	if section == "" {
		return 0
	}
	return strings.Count(section, "\n") + 1
}

func limitFirstLines(text string, maxLines int) string {
	if maxLines <= 0 {
		return text
	}
	lines := multilineLines(text)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:maxLines], "\n")
}

func limitSessionBodyLines(text string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := multilineLines(text)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	if maxLines <= 4 {
		return strings.Join(lines[len(lines)-maxLines:], "\n")
	}
	head := 4
	tail := maxLines - head - 1
	if tail < 1 {
		tail = 1
		head = maxLines - tail - 1
	}
	limited := make([]string, 0, maxLines)
	limited = append(limited, lines[:head]...)
	limited = append(limited, "...")
	limited = append(limited, lines[len(lines)-tail:]...)
	return strings.Join(limited, "\n")
}

func multilineLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
