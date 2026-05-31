package main

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
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

func truncateSessionLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	return ansi.Truncate(line, width, "…")
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
