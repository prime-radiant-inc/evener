package main

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/serf/cmd/serf-tui/internal/pending"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
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
	projectKey  string // server-supplied canonical project ID; empty is non-actionable
	groupKey    string // presentation-only grouping key; never sent to the server
	state       string
	askPending  bool
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
	Ref appwire.Ref
	// EntryIndex is the divergence position: the selected row's
	// transcript.ChatMessage.TranscriptEntryIndex, never a turn id or turn
	// index. thread/fork cuts the child at that entry.
	EntryIndex   int
	OriginalText string
	Label        string
	Submitting   bool
}

type hubModel struct {
	client *appwire.Client
	// frames is the hub connection's ordered notification feed. main wires it
	// to the client before the receive loop starts; a model without one never
	// receives notifications, which is what a model without a hub wants.
	frames *hubFrameFeed
	// dialHub opens a replacement connection after this one dies. A model
	// without one reports the loss and tells the user to restart instead.
	dialHub          hubDialer
	connectionLost   bool
	reconnectAttempt int
	hubURL           string
	stateDir         string
	width            int
	height           int
	err              error

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
	watchedChildRefs        map[string]bool // child transcript refs subscribed for live rail activity
	forkDraft               *hubForkDraft
	sessionThemePicker      *tuipick.ThemePicker
	sessionModelPicker      *tuipick.ModelPicker
	sessionEffortPicker     *tuipick.ModelPicker
	sessionTranscriptPicker *tuipick.ModelPicker
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
	spawnModels             []tuipick.ModelPickerItem
	spawnHarnessModels      map[string][]tuipick.ModelPickerItem
	spawnModelPicker        *tuipick.ModelPicker
	spawnDirInput           textinput.Model
	spawnSubmitting         bool
	spawnFocus              hubSpawnField
	// spawnRecentDirs holds the hub's most recently used project dirs (the
	// Dir field's prepopulated dropdown options, issue #35), fetched with the
	// spawn options. spawnRecentIdx tracks the tab-cycle position: the index
	// of the recent dir currently shown in the field, or -1 when the field
	// holds anything else.
	spawnRecentDirs []string
	spawnRecentIdx  int

	detail  hubSessionDetail
	session model
	notices []noticePanel

	authStatus         authStatus
	authStatusSeen     bool
	sessionStatusError string
	statusRefreshToken int

	authLoginProvider string
	authLoginFlowID   string

	credentialsPanel     *launchconfig.CredentialsPanel
	launchSettingsPanel  *launchconfig.LaunchSettingsPanel
	pluginsPanel         *launchconfig.PluginsPanel
	followupModal        *tuipick.TextInputModal
	launchOverridesModal *launchconfig.LaunchOverridesModal

	// questionOverlay is the ctrl+q-opened ask_user answering flow
	// (question_overlay.go). Opened ONLY by the ctrl+q keypress
	// (toggleAskOverlay) — never by applyHubNotification or any other
	// state change (spec §6.2). Esc defers it (hidden, answers kept)
	// rather than clearing it; see questionOverlay.deferred.
	questionOverlay *questionOverlay

	// escalationsByRef holds in-UI M7 sandbox-exemption approvals keyed by their
	// SESSION ref — a FIFO queue per session (concurrent escalations from one session
	// are supported). It is NOT tied to the viewed session: an escalation for a
	// non-viewed session is still enqueued (never dropped) and surfaced when the user
	// enters that session. The answerable one is the head for the currently-viewed
	// session, answered via a deliberate ctrl+y/ctrl+g chord. Switching away does NOT
	// deny — the queue persists (the daemon's turn-interrupt/close path denies any
	// never-answered one). Never persisted.
	escalationsByRef map[string][]*hubEscalation

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

	// modelRetry holds the in-flight model-call retry the daemon reported on
	// serf/thread/modelRetry (kata 4zn8), or nil when none is pending. Ephemeral
	// chip-strip state, never a transcript line: one rate-limited session logged
	// 91 retries in four hours, and the reader's question ("is this alive, and
	// when does it come back?") is about now, not history. Cleared as soon as
	// the model produces real output.
	modelRetry *appwire.ThreadModelRetryParams

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
	model := hubModel{client: client, hubURL: hubURL, stateDir: stateDir, session: session, browseSelected: -1, dashboardFilter: newHubFilterInput(), dashboardRecentOpen: map[string]bool{}, dashboardProjectClosed: map[string]bool{}, spawnDirInput: newSpawnDirInput(), spawnRecentIdx: -1}
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
	return tea.Batch(fetchHubTree(m.client), waitHubNotification(m.frames))
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
