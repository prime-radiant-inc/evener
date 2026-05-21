package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	pendingAttachments []*PastedImage
	// attachmentSubmitsInFlight counts async submit commands that captured
	// attachment pointers. While non-zero, removed temp files are queued for
	// deferred cleanup so the command can still read them.
	attachmentSubmitsInFlight int
	deferredAttachmentCleanup []*PastedImage
	// nextAttachmentMarker is a per-composer high-water counter. Marker
	// numbers are never reused while a composer draft is alive, even if the
	// user removes the highest-numbered attachment.
	nextAttachmentMarker int
	// clipboardSource is the production clipboard reader, swappable in
	// tests via newSessionHubModel + assignment. When nil we lazily
	// install the platform-specific SystemClipboardSource on first use.
	clipboardSource ClipboardSource

	// pending coordinates optimistic-rendering placeholders for
	// turn/start, turn/queue, turn/steer, turn/drainAsSteer. Wired
	// from main.go via setSend after tea.NewProgram constructs the
	// program reference.
	pending *pendingCoordinator
}

// addPendingAttachment appends a captured image to the composer's
// pending-attachment list. The model cleans up paste-owned temp files when
// the attachment leaves the composer.
//
// The image is assigned a monotonically-increasing MarkerN and the
// literal "[image N]" token is inserted at the textarea's current
// cursor position so the user can reposition or delete it inline. Kata
// 2stz.
func (m *hubModel) addPendingAttachment(img *PastedImage) {
	if img == nil {
		return
	}
	m.nextAttachmentMarker++
	img.MarkerN = m.nextAttachmentMarker
	m.session.input.InsertString("[image " + strconv.Itoa(img.MarkerN) + "]")
	m.pendingAttachments = append(m.pendingAttachments, img)
}

// removePendingAttachment drops the attachment at the given index. Out
// of range indices are silently ignored so handler callsites don't need
// to bounds-check after a re-render race.
//
// If the removed attachment carries a marker, the first occurrence of
// its "[image N]" token is stripped from the textarea. Numbering is not
// renumbered; gaps in the surviving markers are intentional. Kata 2stz.
func (m *hubModel) removePendingAttachment(idx int) {
	if idx < 0 || idx >= len(m.pendingAttachments) {
		return
	}
	removed := m.pendingAttachments[idx]
	if removed != nil && removed.MarkerN > 0 {
		tok := "[image " + strconv.Itoa(removed.MarkerN) + "]"
		text := m.session.input.Value()
		if i := strings.Index(text, tok); i >= 0 {
			m.session.input.SetValue(text[:i] + text[i+len(tok):])
		}
	}
	m.cleanupPendingAttachmentFile(removed)
	m.pendingAttachments = append(m.pendingAttachments[:idx], m.pendingAttachments[idx+1:]...)
}

func (m *hubModel) clearPendingAttachments(cleanupFiles bool) {
	if cleanupFiles {
		for _, img := range m.pendingAttachments {
			m.cleanupPendingAttachmentFile(img)
		}
	}
	m.pendingAttachments = nil
	m.nextAttachmentMarker = 0
}

func (m *hubModel) clearSubmittedAttachments(submitted []*PastedImage, cleanupFiles bool) {
	if len(submitted) == 0 {
		return
	}
	submittedSet := make(map[*PastedImage]struct{}, len(submitted))
	for _, img := range submitted {
		if img == nil {
			continue
		}
		submittedSet[img] = struct{}{}
		if cleanupFiles {
			m.cleanupPendingAttachmentFile(img)
		}
	}
	if len(submittedSet) == 0 {
		return
	}
	kept := m.pendingAttachments[:0]
	for _, img := range m.pendingAttachments {
		if _, ok := submittedSet[img]; ok {
			continue
		}
		kept = append(kept, img)
	}
	m.pendingAttachments = kept
	if len(m.pendingAttachments) == 0 && cleanupFiles {
		m.nextAttachmentMarker = 0
	}
}

func (m *hubModel) restoreSubmittedAttachments(submitted []*PastedImage) {
	if len(submitted) == 0 {
		return
	}
	present := make(map[*PastedImage]struct{}, len(m.pendingAttachments))
	for _, img := range m.pendingAttachments {
		if img != nil {
			present[img] = struct{}{}
		}
	}
	restored := make([]*PastedImage, 0, len(submitted)+len(m.pendingAttachments))
	for _, img := range submitted {
		if img == nil {
			continue
		}
		if _, ok := present[img]; ok {
			continue
		}
		restored = append(restored, img)
		if img.MarkerN > m.nextAttachmentMarker {
			m.nextAttachmentMarker = img.MarkerN
		}
	}
	m.pendingAttachments = append(restored, m.pendingAttachments...)
}

func (m *hubModel) restoreFailedComposerPayload(draft string, submitted []*PastedImage) bool {
	if m.session.input.Value() != "" || len(m.pendingAttachments) > 0 {
		m.clearSubmittedAttachments(submitted, true)
		return false
	}
	m.restoreSubmittedAttachments(submitted)
	m.session.setInputValue(draft)
	return true
}

func (m *hubModel) noteUnrestoredFailedComposerPayload(action, draft string, submitted []*PastedImage) {
	preview := strings.TrimSpace(draft)
	if preview == "" {
		switch len(submitted) {
		case 0:
			return
		case 1:
			preview = "[image]"
		default:
			preview = fmt.Sprintf("[%d images]", len(submitted))
		}
	}
	m.addSessionSystem(fmt.Sprintf("%s failed; preserved current draft instead of restoring failed payload: %s", action, preview))
}

func (m *hubModel) snapshotPendingAttachmentsForSubmit() []*PastedImage {
	if len(m.pendingAttachments) == 0 {
		return nil
	}
	m.attachmentSubmitsInFlight++
	submitted := append([]*PastedImage(nil), m.pendingAttachments...)
	m.clearSubmittedAttachments(submitted, false)
	return submitted
}

func (m *hubModel) finishAttachmentSubmit() {
	if m.attachmentSubmitsInFlight > 0 {
		m.attachmentSubmitsInFlight--
	}
	if m.attachmentSubmitsInFlight != 0 {
		return
	}
	for _, img := range m.deferredAttachmentCleanup {
		cleanupPendingAttachmentFile(img)
	}
	m.deferredAttachmentCleanup = nil
}

func (m *hubModel) cleanupPendingAttachmentFile(img *PastedImage) {
	if m.attachmentSubmitsInFlight > 0 {
		m.deferredAttachmentCleanup = append(m.deferredAttachmentCleanup, img)
		return
	}
	cleanupPendingAttachmentFile(img)
}

func cleanupPendingAttachmentFile(img *PastedImage) {
	if img == nil || img.Path == "" {
		return
	}
	switch img.Origin {
	case "clipboard-image", "wsl":
		_ = os.Remove(img.Path)
	}
}

const hubCtrlCQuitWindow = time.Second

func newHubModel(client *appwire.Client, hubURL string, stateDirs ...string) hubModel {
	stateDir := ""
	if len(stateDirs) > 0 {
		stateDir = strings.TrimSpace(stateDirs[0])
	}
	session := newModel("", "", nil)
	session.authController = nil
	model := hubModel{client: client, hubURL: hubURL, stateDir: stateDir, session: session, browseSelected: -1, dashboardFilter: newHubFilterInput(), dashboardRecentOpen: map[string]bool{}, dashboardProjectClosed: map[string]bool{}, spawnDirInput: newSpawnDirInput()}
	// Construct the pending coordinator with a buffering placeholder
	// send. main.go calls model.pending.setSend(program.Send) after
	// tea.NewProgram so coordinator-emitted msgs reach Update. Until
	// then, msgs are dropped harmlessly (the coordinator only emits
	// in response to user actions, which can't happen pre-Run).
	model.pending = newPendingCoordinator(realClock{}, func(tea.Msg) {})
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
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.session.width = msg.Width
		m.session.height = msg.Height
		m.session.viewport.Width = msg.Width
		m.session.viewport.Height = m.session.vpHeight()
		m.session.refreshViewport()
		m.dashboardFilter.Width = max(1, msg.Width-8)
		return m, nil
	case hubTreeMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.tree = msg.tree
		m.rows = buildDashboardRows(msg.tree)
		if m.mode == hubModeDashboard && !m.dashboardSelectedOnce {
			if m.selected == 0 && len(m.dashboardRows()) > 1 {
				m.selected = 1
			}
			m.dashboardSelectedOnce = true
		}
		m.clampSelection()
		return m, nil
	case hubSessionMsg:
		if msg.err != nil {
			if msg.expectedState != "" && (m.mode != hubModeSession || msg.ref == "" || m.detail.Ref != msg.ref || msg.expectedRefreshToken != m.statusRefreshToken) {
				return m, nil
			}
			m.sessionDetailsRequested = false
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.spawnSubmitting = false
		if msg.expectedState != "" {
			if m.mode != hubModeSession || msg.ref == "" || m.detail.Ref != msg.ref || msg.expectedRefreshToken != m.statusRefreshToken {
				return m, nil
			}
			if !statusRefreshStatesMatchExpected(m.detail.State, msg.detail.State, msg.expectedState) {
				return m, nil
			}
		}
		if m.mode == hubModeSession && m.detail.Ref == msg.detail.Ref {
			m.detail = msg.detail
			if msg.expectedState == "" {
				m.replaceSessionTranscript(msg.messages)
			}
			// Refresh queue preview from the authoritative read response
			// (kata r80p) so reloads / status refreshes resync state.
			m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
			if m.sessionDetailsRequested {
				panel := hubSessionPanel{Body: m.renderSessionDetails()}
				m.sessionPanel = &panel
			}
			m.sessionDetailsRequested = false
			m.session.refreshViewport()
			return m, nil
		}
		m.clearNoticesByCategory("action-unavailable")
		m.clearPendingAttachments(true)
		m.mode = hubModeSession
		m.detail = msg.detail
		m.session = newModel("", "", nil)
		m.session.width = m.width
		m.session.height = m.height
		m.session.sessionID = msg.detail.SessionID
		m.session.sessionModel = msg.detail.Model
		m.session.sessionProfile = msg.detail.Profile
		m.session.processing = appwire.IsActiveThreadStatus(msg.detail.State)
		m.session.messages = msg.messages
		m.session.viewport.Width = m.width
		m.session.viewport.Height = m.session.vpHeight()
		m.session.refreshViewport()
		m.browseSelected = -1
		m.forkDraft = nil
		m.sessionThemePicker = nil
		m.sessionModelPicker = nil
		m.sessionTranscriptPicker = nil
		m.sessionPanel = nil
		m.sessionDetailsRequested = false
		m.transcriptTargets = nil
		m.transcriptView = nil
		// Reset queue scoping when (re)entering a session, then seed from
		// the wire snapshot so the composer chrome reflects authoritative
		// state immediately (kata r80p).
		m.clearSessionQueue()
		m.applyQueueState(msg.detail.Ref, msg.detail.Queue)
		return m, nil
	case hubNotificationMsg:
		if !msg.ok {
			return m, nil
		}
		cmd := m.applyHubNotification(msg.notification)
		return m, tea.Batch(cmd, waitHubNotification(m.client))
	case pendingRegisteredMsg:
		if msg.entry.Ref != "" && !m.matchesAsyncSessionRef(msg.entry.Ref) {
			return m, nil
		}
		switch msg.entry.Method {
		case appwire.MethodTurnSteer:
			m.session.messages = append(m.session.messages, chatMessage{
				Kind:      msgSteering,
				Text:      msg.entry.Text,
				Pending:   true,
				PendingID: msg.entry.ID,
			})
		case appwire.MethodTurnStart:
			m.session.messages = append(m.session.messages, chatMessage{
				Kind:      msgUser,
				Text:      msg.entry.Text,
				Pending:   true,
				PendingID: msg.entry.ID,
			})
		case appwire.MethodTurnDrainAsSteer:
			m.session.messages = append(m.session.messages, chatMessage{
				Kind:      msgSteering,
				Text:      fmt.Sprintf("draining %d → steering", len(m.sessionQueue)),
				Pending:   true,
				PendingID: msg.entry.ID,
			})
		case appwire.MethodTurnQueue:
			// Queue entries surface in the queue-preview chrome, not
			// the transcript pane. No transcript-side placeholder.
		}
		m.session.refreshViewport()
		return m, nil
	case pendingFailedMsg:
		if msg.entry.Ref != "" && !m.matchesAsyncSessionRef(msg.entry.Ref) {
			return m, nil
		}
		m.markPendingFailedByID(msg.entry.ID, msg.reason)
		m.session.refreshViewport()
		return m, nil
	case pendingConfirmedMsg:
		if msg.entry.Ref != "" && !m.matchesAsyncSessionRef(msg.entry.Ref) {
			return m, nil
		}
		m.removePendingByID(msg.entry.ID)
		m.session.refreshViewport()
		return m, nil
	case hubSendMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			// Preserve pendingAttachments on error so the user can retry
			// without re-pasting (kata re91).
			reducer := m.sessionTranscriptReducer()
			reducer.removeUserMessageEcho(msg.text)
			m.applySessionTranscriptReducer(reducer)
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Send", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Send failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Send failed: " + msg.err.Error())
		} else {
			m.clearNoticesByCategory("appwire")
			m.clearSessionError()
			m.setActiveTurnID(msg.turnID)
			m.clearSubmittedAttachments(msg.submittedAttachments, true)
		}
		return m, nil
	case hubQueueMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			// Restore the failed payload only if the composer is still at
			// the post-submit blank state; newer user edits win.
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Queue", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Queue failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Queue failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("appwire")
		m.clearSessionError()
		m.clearSubmittedAttachments(msg.submittedAttachments, true)
		// The daemon emits a thread/queueChanged notification with the
		// new state; applyHubNotification picks it up. No local mirror
		// (kata r80p).
		return m, nil
	case hubDrainAsSteerMsg:
		if msg.trackedAttachmentSubmit {
			m.finishAttachmentSubmit()
		}
		if msg.ref != "" && !m.matchesAsyncSessionRef(msg.ref) {
			if msg.err == nil || msg.queued || isQueuedDrainPartial(msg.err) {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
			}
			return m, nil
		}
		if msg.err != nil {
			if msg.queued || isQueuedDrainPartial(msg.err) {
				m.clearSubmittedAttachments(msg.submittedAttachments, true)
				preview := strings.TrimSpace(msg.text)
				if preview == "" && msg.hadAttachment {
					preview = "[image]"
				}
				if preview != "" && len(m.sessionQueue) <= msg.preQueueDepth {
					m.sessionQueue = append(m.sessionQueue, preview)
					m.session.refreshViewport()
				}
				m.addHubErrorNotice("Force-steer failed after queueing", "appwire", msg.err, "The composer payload was queued already. Retry force-steer without resubmitting the same draft.")
				m.recordSessionError("Force-steer failed after queueing: " + msg.err.Error())
				return m, nil
			}
			if !m.restoreFailedComposerPayload(msg.draft, msg.submittedAttachments) {
				m.noteUnrestoredFailedComposerPayload("Force-steer", msg.draft, msg.submittedAttachments)
			}
			m.addHubErrorNotice("Force-steer failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Force-steer failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("appwire")
		m.clearSessionError()
		m.clearSubmittedAttachments(msg.submittedAttachments, true)
		// The daemon emits thread/queueChanged with depth=0 after a
		// successful drain; applyHubNotification clears sessionQueue when
		// it lands. We don't preemptively wipe here so the preview
		// reflects authoritative state at all times (kata r80p).
		m.addSessionSystem("Force-steer sent.")
		return m, nil
	case hubTasksMsg:
		if msg.err != nil {
			m.addHubErrorNotice("Tasks failed", "appwire", msg.err, "Check the hub connection and retry /tasks.")
			m.recordSessionError("Tasks error: " + msg.err.Error())
		} else {
			m.clearNoticesByCategory("appwire")
			m.clearSessionError()
			m.addSessionSystem(renderTasks(msg.tasks, m.width))
		}
		return m, nil
	case hubStatusMsg:
		if msg.err != nil {
			m.recordSessionError("Status error: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		m.detail = msg.detail
		panel := hubSessionPanel{Body: renderHubSessionStatus(msg.detail, msg.tasks, msg.auth, msg.taskErr, msg.authErr)}
		m.sessionPanel = &panel
		m.session.refreshViewport()
		return m, nil
	case hubActionMsg:
		if msg.err != nil {
			m.addHubErrorNotice("Action failed", "action", msg.err, "Open /help to see source-supported actions.")
			m.recordSessionError(fmt.Sprintf("%s failed: %s", msg.action, msg.err))
			return m, nil
		}
		m.clearNoticesByCategory("action")
		m.clearSessionError()
		switch msg.action {
		case "interrupt":
			m.addSessionSystem("Interrupt sent.")
		case "compact":
			m.addSessionSystem("Context compacted.")
		case "shutdown":
			m.addSessionSystem("Stop requested.")
		case "model":
			m.addSessionSystem("Model updated.")
		case "steer":
			m.addSessionSystem("Steering sent.")
		}
		return m, nil
	case hubClearMsg:
		if msg.err != nil {
			m.recordSessionError("Clear failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Clear returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubForkMsg:
		if msg.err != nil {
			if m.forkDraft != nil {
				m.forkDraft.Submitting = false
			}
			m.recordSessionError("Fork failed: " + msg.err.Error())
			return m, nil
		}
		m.clearSessionError()
		m.forkDraft = nil
		m.session.resetInput()
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Fork returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubSpawnMsg:
		m.spawnSubmitting = false
		if msg.err != nil {
			m.addNotice(noticePanel{
				Title:      "Spawn failed",
				Category:   "launch",
				Summary:    "Hub spawn failed.",
				Source:     m.sourceLabelForNotice(),
				Reason:     msg.err.Error(),
				NextAction: "Check Hub launch diagnostics, auth status, and spawn options.",
			})
			return m, nil
		}
		m.clearNoticesByCategory("launch")
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.err = fmt.Errorf("spawn returned invalid ref: %s", msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubModelsMsg:
		if msg.err != nil {
			if m.mode == hubModeSpawn {
				if msg.harness != "" && !m.spawnHarnessUsesSerfModels() {
					m.err = fmt.Errorf("%s models unavailable; using harness default: %w", msg.harness, msg.err)
				} else {
					m.err = fmt.Errorf("models failed: %w", msg.err)
				}
			}
			return m, nil
		}
		if msg.harness != "" {
			if m.spawnHarnessModels == nil {
				m.spawnHarnessModels = map[string][]modelPickerItem{}
			}
			m.spawnHarnessModels[msg.harness] = msg.models
			if m.mode == hubModeSpawn && m.spawnHarness == msg.harness {
				if len(msg.models) == 0 {
					m.err = fmt.Errorf("no %s models available; using harness default", msg.harness)
					return m, nil
				}
				m.openSpawnModelPicker(msg.models)
			}
			return m, nil
		}
		m.spawnModels = msg.models
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
		}
		return m, nil
	case hubSessionModelsMsg:
		if msg.err != nil {
			m.removeTrailingSessionSystem("Fetching available models...")
			m.addHubErrorNotice("Provider unavailable", "provider", msg.err, "Check provider auth and model availability.")
			return m, nil
		}
		if len(msg.models) == 0 {
			m.removeTrailingSessionSystem("Fetching available models...")
			m.addSessionSystem("No models available from provider.")
			return m, nil
		}
		picker := newModelPicker(msg.models, m.detail.Model, m.width)
		m.sessionModelPicker = &picker
		m.removeTrailingSessionSystem("Fetching available models...")
		return m, nil
	case hubTranscriptTargetsMsg:
		if msg.err != nil {
			m.addSessionSystem("Could not fetch session transcripts: " + msg.err.Error())
			return m, nil
		}
		items := hubTranscriptPickerItems(msg.targets)
		if len(items) == 0 {
			m.addSessionSystem("No session transcripts are available yet.")
			return m, nil
		}
		activeRef := m.detail.Ref
		if m.transcriptView != nil {
			activeRef = m.transcriptView.Ref
		}
		picker := newTranscriptPicker(items, activeRef, m.width)
		m.transcriptTargets = append([]appwire.ThreadTranscriptTarget(nil), msg.targets...)
		m.sessionTranscriptPicker = &picker
		return m, nil
	case hubTranscriptMsg:
		if msg.err != nil {
			m.addSessionSystem("Could not read transcript: " + hubErrorReason(msg.err))
			return m, nil
		}
		m.transcriptView = &hubTranscriptViewState{
			Ref:      msg.target.Ref,
			Title:    msg.target.Title,
			Source:   transcriptTargetSourceLabel(msg.target),
			Messages: msg.messages,
		}
		m.session.scrollMode = true
		m.session.focusedToolIdx = -1
		m.browseSelected = -1
		m.session.input.Blur()
		m.session.refreshViewport()
		return m, nil
	case hubSpawnOptionsMsg:
		if msg.err != nil {
			if m.mode == hubModeSpawn {
				m.err = fmt.Errorf("spawn options failed: %w", msg.err)
			}
			return m, nil
		}
		m.spawnHarnesses = msg.harnesses
		if len(m.spawnHarnesses) == 0 {
			m.spawnHarnesses = []string{"serf"}
		}
		m.spawnHarnessKinds = msg.harnessKinds
		if m.spawnHarnessKinds == nil {
			m.spawnHarnessKinds = map[string]string{}
		}
		m.spawnEmptyTaskReasons = msg.emptyTaskUnsupportedReasons
		if m.spawnEmptyTaskReasons == nil {
			m.spawnEmptyTaskReasons = map[string]string{}
		}
		m.spawnEmptyTaskNext = msg.emptyTaskUnsupportedNext
		if m.spawnEmptyTaskNext == nil {
			m.spawnEmptyTaskNext = map[string]string{}
		}
		for _, harness := range m.spawnHarnesses {
			if m.spawnHarnessKinds[harness] == "" {
				m.spawnHarnessKinds[harness] = "serf"
			}
		}
		if !stringInSlice(m.spawnHarness, m.spawnHarnesses) {
			m.spawnHarness = m.spawnHarnesses[0]
		}
		m.spawnModels = msg.models
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
			if msg.modelErr != nil && m.spawnHarnessUsesSerfModels() {
				m.err = fmt.Errorf("models failed: %w", msg.modelErr)
			}
		}
		return m, nil
	case hubAuthStatusMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Auth status failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authStatus = authStatusFromAppWire(msg.status)
		m.authStatusSeen = true
		m.clearSessionError()
		m.addSessionSystem(formatAuthStatusSummary(m.authStatus))
		return m, nil
	case hubAuthLoginStartMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			return m, nil
		}
		m.authLoginProvider = strings.TrimSpace(msg.resp.Provider)
		if m.authLoginProvider == "" {
			m.authLoginProvider = "openai"
		}
		m.authLoginFlowID = msg.resp.FlowID
		m.addSessionSystem("OpenAI sign-in URL:\n" + msg.resp.URL + "\nPaste the full OpenAI redirect URL and press enter.")
		return m, nil
	case hubAuthLoginCompleteMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Login failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authLoginProvider = ""
		m.authLoginFlowID = ""
		m.authStatus = authStatusFromAppWire(msg.resp.Status)
		m.authStatusSeen = true
		m.clearSessionError()
		m.addSessionSystem("OpenAI login complete. " + formatAuthStatusSummary(m.authStatus))
		return m, nil
	case hubAuthLogoutMsg:
		if msg.err != nil {
			m.addAuthErrorNotice("Auth error", msg.err)
			m.recordSessionError("Logout failed: " + msg.err.Error())
			return m, nil
		}
		m.clearNoticesByCategory("auth")
		m.authStatus = authStatusFromAppWire(msg.resp.Status)
		m.authStatusSeen = true
		m.clearSessionError()
		if msg.resp.Removed {
			m.addSessionSystem("OpenAI sign-out complete. " + formatAuthStatusSummary(m.authStatus))
		} else {
			m.addSessionSystem("OpenAI auth was already signed out. " + formatAuthStatusSummary(m.authStatus))
		}
		return m, nil
	case authListResultMsg:
		if m.credentialsPanel != nil {
			updated, cmd := m.credentialsPanel.Update(msg)
			panel := updated.(credentialsPanel)
			m.credentialsPanel = &panel
			return m, cmd
		}
		return m, nil
	case credentialsActionMsg:
		switch msg.Action {
		case "set":
			modal := newTextInputModalMasked(fmt.Sprintf("API key for %s:", msg.Provider), "credential-set:"+msg.Provider)
			m.followupModal = &modal
			return m, nil
		case "logout":
			if m.client != nil {
				return m, cmdAuthLogout(m.client, msg.Provider)
			}
			return m, nil
		case "oauth":
			if m.client != nil {
				return m, cmdAuthLoginStart(m.client, msg.Provider)
			}
			return m, nil
		}
		return m, nil
	case launchOverridesOpenMsg:
		var modal launchOverridesModal
		if msg.Initial != nil {
			modal = newLaunchOverridesModalWith(*msg.Initial)
		} else {
			modal = newLaunchOverridesModal()
		}
		m.launchOverridesModal = &modal
		return m, nil
	case launchOverridesResultMsg:
		m.launchOverridesModal = nil
		if !msg.Cancelled {
			m.spawnLaunchOverrides = msg.Overrides
		}
		return m, nil
	case launchSettingsEditRequestMsg:
		if msg.Layer == "launch" {
			modal := newTextInputModalWithInput(
				fmt.Sprintf("Edit %s (current: %s):", msg.Field, msg.CurrentValue),
				"launch-override:"+msg.Field,
				msg.CurrentValue,
			)
			m.followupModal = &modal
			return m, nil
		}
		prompt := fmt.Sprintf("Edit %s.%s (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
		tag := fmt.Sprintf("settings-edit:%s:%s", msg.Layer, msg.Field)
		var modal textInputModal
		if launchSettingsFieldUsesPathCompletion(msg.Field) {
			modal = newPathTextInputModal(prompt, tag, msg.CurrentValue)
		} else {
			modal = newTextInputModalWithInput(prompt, tag, msg.CurrentValue)
		}
		m.followupModal = &modal
		return m, nil
	case textInputResultMsg:
		if strings.HasPrefix(msg.Tag, "credential-set:") {
			provider := strings.TrimPrefix(msg.Tag, "credential-set:")
			m.followupModal = nil
			if msg.Cancelled || msg.Value == "" {
				return m, nil
			}
			if m.client != nil {
				return m, cmdAuthApiKeySet(m.client, provider, msg.Value)
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "oauth-redirect:") {
			parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "oauth-redirect:"), ":", 2)
			m.followupModal = nil
			if msg.Cancelled || msg.Value == "" {
				return m, nil
			}
			if len(parts) == 2 && m.client != nil {
				return m, cmdAuthLoginComplete(m.client, parts[0], parts[1], msg.Value)
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "launch-override:") {
			field := strings.TrimPrefix(msg.Tag, "launch-override:")
			m.followupModal = nil
			if msg.Cancelled {
				return m, nil
			}
			if m.launchOverridesModal != nil {
				updated, err := m.launchOverridesModal.ApplyEdit(field, msg.Value)
				if err != nil {
					m.err = err
					return m, nil
				}
				m.launchOverridesModal = &updated
			}
			return m, nil
		}
		if strings.HasPrefix(msg.Tag, "settings-edit:") {
			parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "settings-edit:"), ":", 2)
			if len(parts) != 2 {
				return m, nil
			}
			layer, field := parts[0], parts[1]
			m.followupModal = nil
			if msg.Cancelled {
				return m, nil
			}
			if m.launchSettingsPanel == nil {
				return m, nil
			}
			panel, updatedLayer, err := m.launchSettingsPanel.ApplyEdit(field, msg.Value)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.launchSettingsPanel = &panel
			return m, cmdSetLayer(m.client, panel.cwd, layer, updatedLayer)
		}
		return m, nil
	case authApiKeySetResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		if m.credentialsPanel != nil && m.client != nil {
			return m, cmdAuthList(m.client)
		}
		return m, nil
	case authLoginStartResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		modal := newTextInputModal("Paste full redirect URL after sign-in:\n"+msg.URL, "oauth-redirect:"+msg.Provider+":"+msg.FlowID)
		m.followupModal = &modal
		return m, nil
	case authLoginCompleteResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.err = nil
		if m.credentialsPanel != nil && m.client != nil {
			return m, cmdAuthList(m.client)
		}
		return m, nil
	case launchSetLayerResultMsg:
		if m.launchSettingsPanel != nil {
			updated, cmd := m.launchSettingsPanel.Update(msg)
			p := updated.(launchSettingsPanel)
			m.launchSettingsPanel = &p
			if msg.Err == nil && m.client != nil {
				// Refresh the just-saved layer from disk.
				return m, tea.Batch(cmd, cmdGetLayer(m.client, msg.CWD, msg.Layer))
			}
			return m, cmd
		}
		return m, nil
	case launchLayerResultMsg, launchResolveResultMsg, launchTrustResultMsg:
		if m.launchSettingsPanel != nil {
			updated, cmd := m.launchSettingsPanel.Update(msg)
			p := updated.(launchSettingsPanel)
			m.launchSettingsPanel = &p
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

func statusRefreshStatesMatchExpected(currentState, payloadState, expectedState string) bool {
	return currentState == expectedState && payloadState == expectedState
}

func (m hubModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+x" && len(m.notices) > 0 {
		m.dismissNotice()
		return m, nil
	}
	switch msg.String() {
	case "ctrl+o":
		m.returnToDashboard()
		return m, nil
	case "ctrl+c":
		if m.mode == hubModeSession && m.commandPalette == nil {
			return m.updateSessionKey(msg)
		}
		return m, tea.Quit
	}
	if m.commandPalette != nil {
		return m.updateCommandPaletteKey(msg)
	}
	if m.mode == hubModeSession {
		return m.updateSessionKey(msg)
	}
	if m.mode == hubModeSpawn {
		return m.updateSpawnKey(msg)
	}
	return m.updateDashboardKey(msg)
}

func (m hubModel) updateDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil {
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(textInputModal)
		m.followupModal = &modal
		if modal.done {
			m.followupModal = nil
		}
		return m, cmd
	}
	if m.credentialsPanel != nil {
		updated, cmd := m.credentialsPanel.Update(msg)
		panel := updated.(credentialsPanel)
		m.credentialsPanel = &panel
		if panel.done {
			m.credentialsPanel = nil
		}
		return m, cmd
	}
	if m.launchSettingsPanel != nil {
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchSettingsPanel)
		m.launchSettingsPanel = &p
		if p.done {
			m.launchSettingsPanel = nil
			return m, nil
		}
		return m, cmd
	}
	if m.dashboardFilterActive {
		return m.updateHubFilterKey(msg)
	}
	rows := m.dashboardRows()
	switch msg.String() {
	case "/":
		m.openCommandPalette()
		return m, nil
	case "q":
		return m, tea.Quit
	case "r":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
	case "n":
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client, m.spawnDir)
		}
		return m, nil
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(rows)-1 {
			m.selected++
		}
	case "right", "l":
		m.setSelectedDashboardProjectExpanded(rows, true)
	case "left", "h":
		m.setSelectedDashboardProjectExpanded(rows, false)
	case "enter":
		return m.activateDashboardRow(rows)
	}
	return m, nil
}

func (m hubModel) updateHubFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dashboardFilter.Reset()
		m.dashboardFilter.Blur()
		m.dashboardFilterActive = false
		m.clampSelection()
		return m, nil
	case "enter":
		m.dashboardFilter.Blur()
		m.dashboardFilterActive = false
		m.clampSelection()
		return m.activateDashboardRow(m.dashboardRows())
	}
	var cmd tea.Cmd
	m.dashboardFilter, cmd = m.dashboardFilter.Update(msg)
	m.clampSelection()
	return m, cmd
}

func (m hubModel) activateDashboardRow(rows []hubRow) (tea.Model, tea.Cmd) {
	if len(rows) == 0 {
		return m, nil
	}
	row := rows[m.selected]
	switch row.kind {
	case hubRowLaunch:
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client, m.spawnDir)
		}
		return m, nil
	case hubRowRecentToggle:
		m.toggleDashboardRecent(row.projectKey)
		m.clampSelection()
		return m, nil
	case hubRowProject:
		m.toggleDashboardProject(row.projectKey)
		m.clampSelection()
		return m, nil
	}
	if m.client == nil {
		return m, nil
	}
	return m, fetchHubSession(m.client, row.ref)
}

func (m *hubModel) enterHubFilter() {
	m.dashboardFilterActive = true
	m.dashboardFilter.Focus()
	m.clampSelection()
}

func (m *hubModel) openCommandPalette() {
	width := m.width
	if width <= 0 {
		width = 100
	}
	rows := m.dashboardRows()
	if m.mode == hubModeSession {
		rows = nil
	}
	palette := newCommandPalette("Command palette", commandPaletteEntriesForRows(m.mode, m.detail.Capabilities, rows), width)
	m.commandPalette = &palette
}

func (m hubModel) updateCommandPaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.commandPalette == nil {
		return m, nil
	}
	updated, cmd := m.commandPalette.Update(msg)
	palette := updated.(commandPalette)
	m.commandPalette = &palette
	if !palette.panel.done {
		return m, cmd
	}
	m.commandPalette = nil
	if palette.panel.cancelled {
		return m, cmd
	}
	entry, ok := palette.selectedEntry()
	if !ok {
		return m, cmd
	}
	switch entry.Kind {
	case commandPaletteCommand:
		return m.runCommandPaletteCommand(entry.Command)
	case commandPaletteProject:
		m.focusDashboardProject(entry.ProjectKey)
		return m, nil
	case commandPaletteSession:
		if m.client == nil {
			return m, nil
		}
		return m, fetchHubSession(m.client, entry.Ref)
	default:
		return m, cmd
	}
}

func (m hubModel) runCommandPaletteCommand(command string) (tea.Model, tea.Cmd) {
	definition, ok := hubCommandByName(command)
	if !ok {
		return m, nil
	}
	available, _ := hubCommandAvailable(definition, hubCommandContext{mode: m.mode, caps: m.detail.Capabilities})
	if !available {
		return m, nil
	}
	cmd := runHubCommandDefinition(&m, definition, "")
	return m, cmd
}

func (m hubModel) updateSpawnKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil && m.launchOverridesModal != nil {
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(textInputModal)
		m.followupModal = &modal
		if modal.done {
			m.followupModal = nil
		}
		return m, cmd
	}

	if m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchOverridesModal)
		m.launchOverridesModal = &p
		if p.done {
			m.launchOverridesModal = nil
		}
		return m, cmd
	}

	if m.spawnModelPicker != nil {
		updated, cmd := m.spawnModelPicker.Update(msg)
		picker := updated.(modelPicker)
		m.spawnModelPicker = &picker
		if picker.done {
			m.spawnModelPicker = nil
			if picker.selected != "" {
				m.spawnModel = picker.selected
			}
		}
		return m, cmd
	}

	if msg.Type == tea.KeyCtrlL {
		var initial *appwire.LaunchConfigLayer
		if m.spawnLaunchOverrides != nil {
			cp := *m.spawnLaunchOverrides
			initial = &cp
		}
		return m, func() tea.Msg { return launchOverridesOpenMsg{Initial: initial} }
	}

	switch msg.String() {
	case "esc":
		m.closeSpawnForm()
		return m, nil
	case "tab":
		m.advanceSpawnFocus(1)
		return m, nil
	case "shift+tab":
		m.advanceSpawnFocus(-1)
		return m, nil
	case "enter":
		switch m.spawnFocus {
		case hubSpawnFieldHarness:
			m.cycleSpawnHarness()
			return m, nil
		case hubSpawnFieldModel:
			return m.activateSpawnModelField()
		case hubSpawnFieldDir:
			m.advanceSpawnFocus(1)
			return m, nil
		default:
			return m.submitSpawnForm()
		}
	case " ":
		if m.spawnFocus == hubSpawnFieldHarness {
			m.cycleSpawnHarness()
			return m, nil
		}
		if m.spawnFocus == hubSpawnFieldModel {
			return m.activateSpawnModelField()
		}
	}

	if m.spawnFocus == hubSpawnFieldDir {
		if msg.Type == tea.KeyCtrlU {
			m.setSpawnDir("")
			return m, nil
		}
		var cmd tea.Cmd
		m.spawnDirInput, cmd = m.spawnDirInput.Update(msg)
		m.spawnDir = strings.TrimSpace(m.spawnDirInput.Value())
		return m, cmd
	}

	if m.spawnFocus != hubSpawnFieldPrompt {
		return m, nil
	}

	if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
		m.session.input.InsertString("\n")
		m.resizeSpawnInput()
		return m, nil
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSpawnInputFrom(prevHeight)
	return m, cmd
}

func (m hubModel) activateSpawnModelField() (tea.Model, tea.Cmd) {
	models := m.spawnSelectableModels()
	if len(models) == 0 && !m.spawnHarnessUsesSerfModels() && m.client != nil {
		m.err = nil
		return m, fetchHubModelsForHarness(m.client, m.spawnHarness, m.spawnDir)
	}
	if len(models) == 0 {
		if !m.spawnHarnessUsesSerfModels() {
			m.err = fmt.Errorf("no %s models available; using harness default", m.spawnHarness)
		} else {
			m.err = fmt.Errorf("no models available")
		}
		return m, nil
	}
	m.openSpawnModelPicker(models)
	return m, nil
}

func (m hubModel) submitSpawnForm() (tea.Model, tea.Cmd) {
	if m.client == nil || m.spawnSubmitting {
		return m, nil
	}
	prompt := strings.TrimSpace(m.session.input.Value())
	if prompt == "" {
		if reason := m.spawnEmptyTaskUnsupportedReason(); reason != "" {
			m.err = fmt.Errorf("%s", noticePanel{
				Title:      "Spawn unavailable",
				Source:     strings.TrimSpace(m.spawnHarness),
				Reason:     reason,
				NextAction: m.spawnEmptyTaskUnsupportedNextAction(),
			}.Text())
			return m, nil
		}
	}
	if m.spawnHarnessUsesSerfModels() && strings.TrimSpace(m.spawnModel) == "" {
		m.err = fmt.Errorf("choose a model before spawning")
		return m, nil
	}
	if reason := m.spawnModelDisabledReason(strings.TrimSpace(m.spawnModel)); reason != "" {
		m.err = fmt.Errorf("%s", noticePanel{
			Title:      "Spawn unavailable",
			Source:     strings.TrimSpace(m.spawnHarness),
			Reason:     "selected model is not available: " + reason,
			NextAction: "choose an enabled model or resolve the provider requirement",
		}.Text())
		return m, nil
	}
	req := hubSpawnRequest{
		Prompt:          prompt,
		Harness:         strings.TrimSpace(m.spawnHarness),
		Model:           strings.TrimSpace(m.spawnModel),
		WorkingDir:      strings.TrimSpace(m.spawnDir),
		LaunchOverrides: m.spawnLaunchOverrides,
	}
	m.err = nil
	m.spawnSubmitting = true
	m.spawnLaunchOverrides = nil // one-shot: clear after use
	return m, sendHubSpawn(m.client, req)
}

func (m *hubModel) setSpawnFocus(field hubSpawnField) {
	if field < hubSpawnFieldPrompt || field > hubSpawnFieldDir {
		field = hubSpawnFieldPrompt
	}
	m.spawnFocus = field
	if field == hubSpawnFieldPrompt {
		m.session.input.Focus()
		m.spawnDirInput.Blur()
		return
	}
	m.session.input.Blur()
	if field == hubSpawnFieldDir {
		if strings.TrimSpace(m.spawnDirInput.Value()) == "" && strings.TrimSpace(m.spawnDir) != "" {
			m.spawnDirInput.SetValue(strings.TrimSpace(m.spawnDir))
		}
		m.spawnDirInput.Focus()
		return
	}
	m.spawnDirInput.Blur()
}

func (m *hubModel) advanceSpawnFocus(delta int) {
	next := int(m.spawnFocus) + delta
	count := int(hubSpawnFieldDir) + 1
	for next < 0 {
		next += count
	}
	next %= count
	m.setSpawnFocus(hubSpawnField(next))
}

func (m *hubModel) resizeSpawnInput() {
	m.resizeSpawnInputFrom(m.session.input.Height())
}

func (m *hubModel) resizeSpawnInputFrom(prevHeight int) {
	wantHeight := m.session.input.LineCount()
	if wantHeight < 1 {
		wantHeight = 1
	}
	if wantHeight > m.session.input.MaxHeight {
		wantHeight = m.session.input.MaxHeight
	}
	if wantHeight != prevHeight {
		m.session.input.SetHeight(wantHeight)
	}
}

func (m *hubModel) resizeSessionInputFrom(prevHeight int) {
	wantHeight := m.session.input.LineCount()
	if wantHeight < 1 {
		wantHeight = 1
	}
	if wantHeight > m.session.input.MaxHeight {
		wantHeight = m.session.input.MaxHeight
	}
	if wantHeight != prevHeight {
		m.session.input.SetHeight(wantHeight)
		m.session.viewport.Height = m.session.vpHeight()
	}
}

func (m hubModel) spawnFieldPrefix(field hubSpawnField) string {
	if m.spawnFocus == field {
		return ">"
	}
	return " "
}

func (m hubModel) spawnFieldHint() string {
	switch m.spawnFocus {
	case hubSpawnFieldHarness:
		return "enter/space: change harness"
	case hubSpawnFieldModel:
		if !m.spawnHarnessUsesSerfModels() && len(m.spawnSelectableModels()) == 0 {
			return "enter: fetch harness models"
		}
		return "enter: choose model"
	case hubSpawnFieldDir:
		return "type path  enter: next  ctrl+u clear"
	default:
		return "enter: spawn  ctrl+j: newline"
	}
}

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil && m.launchOverridesModal != nil {
		// followupModal is open for a launch-override edit — route to it
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(textInputModal)
		m.followupModal = &modal
		if modal.done {
			m.followupModal = nil
		}
		return m, cmd
	}

	if m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchOverridesModal)
		m.launchOverridesModal = &p
		if p.done {
			m.launchOverridesModal = nil
		}
		return m, cmd
	}

	if m.sessionThemePicker != nil {
		picker, cmd := m.sessionThemePicker.Update(msg)
		m.sessionThemePicker = &picker
		if picker.done {
			m.sessionThemePicker = nil
			if picker.selected != "" {
				setThemeAndPersist(m.stateDir, picker.selected)
				initMarkdownRenderer(m.width)
				m.session.viewport.Style = viewportStyle
				applyInputTheme(&m.session.input)
				m.addSessionSystem(fmt.Sprintf("Switched to %s theme.", picker.selected))
			} else {
				m.session.refreshViewport()
			}
		}
		return m, cmd
	}

	if m.sessionModelPicker != nil {
		updated, cmd := m.sessionModelPicker.Update(msg)
		picker := updated.(modelPicker)
		m.sessionModelPicker = &picker
		if picker.done {
			selected := picker.selected
			m.sessionModelPicker = nil
			if selected != "" {
				ref, ok := m.currentRef()
				if !ok {
					m.addSessionSystem("Session ref is invalid.")
					return m, nil
				}
				m.addSessionSystem(fmt.Sprintf("Switching to model %s...", selected))
				return m, sendHubAction(m.client, ref, selected, "")
			}
			m.session.refreshViewport()
		}
		return m, cmd
	}

	if m.sessionTranscriptPicker != nil {
		updated, cmd := m.sessionTranscriptPicker.Update(msg)
		picker := updated.(modelPicker)
		m.sessionTranscriptPicker = &picker
		if picker.done {
			selected := picker.selected
			m.sessionTranscriptPicker = nil
			if selected != "" {
				target, ok := hubTranscriptTargetByRef(m.transcriptTargets, selected)
				if !ok {
					m.addSessionSystem("Transcript target is no longer available.")
					return m, nil
				}
				if target.Kind == "main" {
					m.transcriptView = nil
					m.session.scrollMode = false
					m.session.input.Focus()
					m.session.refreshViewport()
					return m, nil
				}
				return m, fetchHubTranscript(m.client, target)
			}
			m.session.refreshViewport()
		}
		return m, cmd
	}

	if m.sessionPanel != nil && msg.String() == "esc" {
		m.sessionPanel = nil
		m.session.refreshViewport()
		return m, nil
	}

	if m.transcriptView != nil {
		switch msg.String() {
		case "esc", "i", "q":
			m.transcriptView = nil
			m.session.scrollMode = false
			m.session.focusedToolIdx = -1
			m.browseSelected = -1
			m.session.input.Focus()
			m.session.refreshViewport()
		default:
			m.session.viewport, _ = m.session.viewport.Update(msg)
		}
		return m, nil
	}

	if m.forkDraft != nil {
		switch msg.String() {
		case "esc":
			m.forkDraft = nil
			m.session.resetInput()
			m.enterSessionBrowse(false)
			m.addSessionSystem("Fork cancelled.")
			return m, nil
		case "enter":
			if m.forkDraft.Submitting {
				return m, nil
			}
			text := strings.TrimSpace(m.session.input.Value())
			if text == "" {
				m.addSessionSystem("Fork message cannot be empty.")
				return m, nil
			}
			if m.client == nil {
				m.addSessionSystem("Fork is not available without a hub client.")
				return m, nil
			}
			draft := *m.forkDraft
			m.forkDraft.Submitting = true
			m.addSessionSystem(fmt.Sprintf("Forking from turn %d...", draft.Turn))
			return m, sendHubFork(m.client, draft.Ref, hubForkRequest{
				Turn:          draft.Turn,
				EditedMessage: text,
				Label:         draft.Label,
			})
		}
	}

	if m.session.scrollMode {
		switch msg.String() {
		case "esc", "i", "q":
			m.exitSessionBrowse()
		case "up", "k":
			m.moveBrowseSelection(-1)
		case "down", "j":
			m.moveBrowseSelection(1)
		case "pgup":
			m.moveBrowsePage(-1)
		case "pgdown":
			m.moveBrowsePage(1)
		case "f":
			m.startForkDraft()
		case "tab", "enter":
			if m.browseSelected >= 0 && m.browseSelected < len(m.session.messages) {
				msg := &m.session.messages[m.browseSelected]
				if msg.Kind == msgTool && msg.Tool != nil && msg.Tool.Done {
					msg.Tool.Expanded = !msg.Tool.Expanded
					m.session.refreshViewport()
					m.session.scrollToMessage(m.browseSelected)
				}
			}
		default:
			m.session.viewport, _ = m.session.viewport.Update(msg)
		}
		return m, nil
	}

	if strings.TrimSpace(m.authLoginFlowID) != "" {
		switch msg.String() {
		case "esc":
			m.authLoginProvider = ""
			m.authLoginFlowID = ""
			m.session.resetInput()
			m.addSessionSystem("OpenAI login cancelled.")
			return m, nil
		case "enter":
			redirectURL := strings.TrimSpace(m.session.input.Value())
			if redirectURL == "" {
				return m, nil
			}
			provider := m.authLoginProvider
			flowID := m.authLoginFlowID
			m.session.resetInput()
			m.addSessionSystem("Finishing OpenAI login...")
			return m, completeHubAuthLogin(m.client, provider, flowID, redirectURL)
		}
	}

	if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
		prevHeight := m.session.input.Height()
		m.session.input.InsertString("\n")
		m.resizeSessionInputFrom(prevHeight)
		return m, nil
	}
	if msg.String() == "/" && strings.TrimSpace(m.session.input.Value()) == "" {
		m.openCommandPalette()
		return m, nil
	}
	if msg.Type == tea.KeyCtrlP || msg.String() == "ctrl+p" {
		m.openCommandPalette()
		return m, nil
	}
	if msg.Type == tea.KeyCtrlL {
		var initial *appwire.LaunchConfigLayer
		if m.spawnLaunchOverrides != nil {
			cp := *m.spawnLaunchOverrides
			initial = &cp
		}
		return m, func() tea.Msg { return launchOverridesOpenMsg{Initial: initial} }
	}
	if msg.Type == tea.KeyCtrlS {
		return m.handleSessionForceSteer()
	}
	if msg.Type == tea.KeyCtrlV || isAltVKey(msg) {
		return m.handleClipboardPaste()
	}
	// Alt+Backspace removes the most recently added attachment chip
	// (kata 5vxd). Plain Ctrl-H is not safe here because many terminals
	// report ordinary Backspace as Ctrl-H.
	if msg.Alt && (msg.Type == tea.KeyBackspace || msg.Type == tea.KeyCtrlH) && len(m.pendingAttachments) > 0 {
		m.removePendingAttachment(len(m.pendingAttachments) - 1)
		return m, nil
	}
	if msg.Paste {
		if cmd, handled := m.handleBracketedPaste(string(msg.Runes)); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		now := time.Now()
		if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) <= hubCtrlCQuitWindow {
			m.postQuitMessage = m.restoreInstructionMessage()
			return m, tea.Quit
		}
		m.lastCtrlC = now
		return m, nil
	case "esc":
		m.enterSessionBrowse(false)
		return m, nil
	case "up":
		if len(m.session.history) > 0 {
			if m.session.historyIdx >= 0 {
				if m.session.historyIdx > 0 {
					m.session.historyIdx--
				}
			} else if m.session.input.Value() == "" {
				m.session.historyDraft = m.session.input.Value()
				m.session.historyIdx = len(m.session.history) - 1
			} else {
				return m, nil
			}
			m.session.setInputValue(unescapeHistory(m.session.history[m.session.historyIdx]))
			return m, nil
		}
	case "down":
		if m.session.historyIdx >= 0 {
			if m.session.historyIdx < len(m.session.history)-1 {
				m.session.historyIdx++
				m.session.setInputValue(unescapeHistory(m.session.history[m.session.historyIdx]))
			} else {
				m.session.historyIdx = -1
				m.session.setInputValue(m.session.historyDraft)
				m.session.historyDraft = ""
			}
			return m, nil
		}
	case "pgup":
		m.enterSessionBrowse(true)
		return m, nil
	}

	if msg.String() == "enter" {
		draft := m.session.input.Value()
		text := strings.TrimSpace(draft)
		if text == "" && len(m.pendingAttachments) == 0 {
			return m, nil
		}
		if text != "" {
			if cmd, args := parseSlashCommand(text); cmd != "" {
				m.session.resetInput()
				next := m.runHubSlashCommand(cmd, args)
				return m, next
			}
		}
		composerMode := m.sessionComposerMode()
		if composerMode == hubComposerModeQueue {
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return m, nil
			}
			// Clear the composer optimistically; the queue preview above
			// the composer will show the enqueued line. On failure the
			// hubQueueMsg handler restores the draft.
			if text != "" {
				m.session.addHistory(text)
			}
			m.session.resetInput()
			m.session.refreshViewport()
			attachments := m.snapshotPendingAttachmentsForSubmit()
			return m, sendHubQueue(m.client, ref, text, draft, attachments)
		}
		if composerMode == hubComposerModeReadOnly || !m.sessionCanStartTurn() {
			reason := m.sessionComposerReadOnlyReason()
			if reason == "" {
				reason = "send is not available for this session"
			}
			m.addActionUnavailableNotice("send", "Send is not available for this session.", reason)
			return m, nil
		}
		ref, ok := m.currentRef()
		if !ok {
			m.addSessionSystem("Session ref is invalid.")
			return m, nil
		}
		reducer := m.sessionTranscriptReducer()
		reducer.applyUserMessageEcho(text)
		m.applySessionTranscriptReducer(reducer)
		m.session.lastSentText = text
		if text != "" {
			m.session.addHistory(text)
		}
		m.session.resetInput()
		m.session.refreshViewport()
		attachments := m.snapshotPendingAttachmentsForSubmit()
		return m, sendHubInput(m.client, ref, text, draft, attachments)
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSessionInputFrom(prevHeight)
	return m, cmd
}

func (m *hubModel) runHubSlashCommand(cmd, args string) tea.Cmd {
	definition, ok := hubCommandByName(cmd)
	if !ok || definition.Scopes&hubCommandSession == 0 {
		m.addSessionSystem("Unknown command: /" + cmd + ". Type /help for available commands.")
		return nil
	}
	available, reason := hubCommandAvailable(definition, hubCommandContext{mode: hubModeSession, caps: m.detail.Capabilities})
	if !available {
		m.addActionUnavailableNotice(definition.UnavailableAction, definition.UnavailableSummary, reason)
		return nil
	}
	return runHubCommandDefinition(m, definition, args)
}

func (m hubModel) ctrlCRestoreMessage() string {
	restore := m.restoreInstructionMessage()
	if restore == "" {
		return "Press ctrl+c again to quit."
	}
	return "Press ctrl+c again to quit.\n" + restore
}

func (m hubModel) restoreInstructionMessage() string {
	hubURL := strings.TrimSpace(m.hubURL)
	if hubURL == "" {
		hubURL = defaultHubAddr
	}
	ref := strings.TrimSpace(m.detail.Ref)
	if ref == "" {
		ref = strings.TrimSpace(m.session.sessionID)
	}
	if ref == "" {
		return ""
	}
	return fmt.Sprintf("Restore this session: serf-tui --hub-addr %s, then open %s", hubURL, ref)
}

// handleSessionForceSteer routes the Ctrl+S force-steer keybind: drain
// every queued message into a single STEERING entry for the in-flight turn
// (kata 0bq1). If the composer has unsent text or attachments, they ride on
// the drain request so the daemon appends and drains atomically. With nothing
// to steer, the binding fires a transient banner instead of calling the hub.
func (m hubModel) handleSessionForceSteer() (tea.Model, tea.Cmd) {
	if m.sessionComposerMode() != hubComposerModeQueue {
		// Not in a queue-able state; nothing to do. Silently no-op so the
		// keybind doesn't fight with idle-state composing.
		return m, nil
	}
	if !m.detail.Capabilities.Steer {
		m.addSessionSystem("Force-steer is not available: source does not advertise steer.")
		return m, nil
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return m, nil
	}
	draft := m.session.input.Value()
	pending := strings.TrimSpace(draft)
	hasAttachments := len(m.pendingAttachments) > 0
	if pending == "" && len(m.sessionQueue) == 0 && !hasAttachments {
		m.addSessionSystem("Nothing to steer: the queue is empty.")
		return m, nil
	}
	if pending == "" && !hasAttachments {
		// Pure drain of the existing queue. Clear nothing on the composer.
		return m, sendHubDrainAsSteer(m.client, ref, "", "", nil, len(m.sessionQueue))
	}
	// Composer has text and/or attachments. sendHubDrainAsSteer sends the
	// payload on turn/drainAsSteer so the daemon folds it into the same
	// STEERING entry as everything already queued.
	if pending != "" {
		m.session.addHistory(pending)
	}
	m.session.resetInput()
	m.session.refreshViewport()
	attachments := m.snapshotPendingAttachmentsForSubmit()
	return m, sendHubDrainAsSteer(m.client, ref, pending, draft, attachments, len(m.sessionQueue))
}

func isQueuedDrainPartial(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.SerfErrorInfo == appwire.ErrorQueuedDrainPartial
	case map[string]any:
		return data["serfErrorInfo"] == string(appwire.ErrorQueuedDrainPartial)
	default:
		return false
	}
}

// isAltVKey reports whether the keypress is Alt+v / Ctrl+Alt+V. WSL
// terminals frequently swallow Ctrl+V on the Windows side, so we accept
// Alt+v as the equivalent shortcut for clipboard paste.
func isAltVKey(msg tea.KeyMsg) bool {
	if !msg.Alt {
		return false
	}
	if msg.Type != tea.KeyRunes {
		return false
	}
	if len(msg.Runes) != 1 {
		return false
	}
	r := msg.Runes[0]
	return r == 'v' || r == 'V'
}

// handleClipboardPaste reads an image from the system clipboard and
// pushes it onto pendingAttachments. On failure the user gets a
// system-message banner explaining the cause instead of a silent miss.
func (m hubModel) handleClipboardPaste() (tea.Model, tea.Cmd) {
	src := m.clipboardSource
	if src == nil {
		src = NewSystemClipboardSource()
		m.clipboardSource = src
	}
	img, err := PasteClipboardImage(src)
	if err != nil {
		m.addSessionSystem("Clipboard paste failed: " + err.Error())
		return m, nil
	}
	m.addPendingAttachment(img)
	return m, nil
}

// handleBracketedPaste inspects bracketed-paste payloads for the
// "single image path" shape. When the text resolves to an existing
// image file, the path is attached and the textarea is left alone;
// otherwise the caller falls through and the textarea receives the
// paste as normal text.
func (m *hubModel) handleBracketedPaste(text string) (tea.Cmd, bool) {
	resolved := NormalizePastedPath(text)
	if resolved == "" {
		return nil, false
	}
	if !IsImageFile(resolved) {
		return nil, false
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return nil, false
	}
	m.addPendingAttachment(&PastedImage{
		Path:      resolved,
		MediaType: mediaTypeForPath(resolved),
		Size:      int(info.Size()),
		Origin:    "path",
	})
	return nil, true
}

func (m *hubModel) enterSessionBrowse(pageUp bool) {
	m.session.scrollMode = true
	m.session.focusedToolIdx = -1
	m.session.input.Blur()
	if m.browseSelected < 0 || m.browseSelected >= len(m.session.messages) {
		m.browseSelected = m.lastBrowseMessageIndex()
	}
	if pageUp {
		m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
}

func (m *hubModel) exitSessionBrowse() {
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.browseSelected = -1
	m.session.input.Focus()
}

func (m *hubModel) returnToDashboard() {
	if m.mode == hubModeSpawn {
		m.resetSpawnForm()
	}
	m.commandPalette = nil
	m.sessionThemePicker = nil
	m.sessionModelPicker = nil
	m.sessionTranscriptPicker = nil
	m.transcriptTargets = nil
	m.transcriptView = nil
	m.forkDraft = nil
	m.spawnModelPicker = nil
	m.credentialsPanel = nil
	m.launchSettingsPanel = nil
	m.followupModal = nil
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.browseSelected = -1
	m.authLoginProvider = ""
	m.authLoginFlowID = ""
	m.clearSessionQueue()
	m.mode = hubModeDashboard
	m.clampSelection()
}

func (m *hubModel) openSpawnForm() {
	returnMode := m.mode
	dir := m.spawnWorkingDir()
	project := m.spawnProjectName()
	m.resetSpawnForm()
	m.spawnReturnMode = returnMode
	m.setSpawnDir(dir)
	m.spawnProject = project
	m.mode = hubModeSpawn
	m.err = nil
	m.setSpawnFocus(hubSpawnFieldPrompt)
}

func (m *hubModel) closeSpawnForm() {
	m.resetSpawnForm()
	m.mode = hubModeDashboard
	m.clampSelection()
}

func (m *hubModel) resetSpawnForm() {
	m.spawnReturnMode = hubModeDashboard
	m.setSpawnDir("")
	m.spawnProject = ""
	m.spawnHarness = "serf"
	m.spawnHarnesses = []string{"serf"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf"}
	m.spawnEmptyTaskReasons = nil
	m.spawnEmptyTaskNext = nil
	m.spawnModel = ""
	m.spawnModels = nil
	m.spawnHarnessModels = nil
	m.spawnModelPicker = nil
	m.spawnSubmitting = false
	m.spawnFocus = hubSpawnFieldPrompt
	m.spawnDirInput.Blur()
	m.session.resetInput()
	if envModel := strings.TrimSpace(os.Getenv("SERF_MODEL")); strings.Contains(envModel, "/") {
		m.spawnModel = envModel
	}
}

func (m *hubModel) setSpawnDir(dir string) {
	dir = strings.TrimSpace(dir)
	m.spawnDir = dir
	m.spawnDirInput = newSpawnDirInput()
	m.spawnDirInput.SetValue(dir)
	if m.spawnFocus == hubSpawnFieldDir {
		m.spawnDirInput.Focus()
	}
}

func (m *hubModel) cycleSpawnHarness() {
	if len(m.spawnHarnesses) == 0 {
		m.spawnHarnesses = []string{"serf"}
	}
	for i, harness := range m.spawnHarnesses {
		if harness == m.spawnHarness {
			m.spawnHarness = m.spawnHarnesses[(i+1)%len(m.spawnHarnesses)]
			m.spawnModel = ""
			m.spawnModelPicker = nil
			m.syncSpawnModelWithHarness()
			return
		}
	}
	m.spawnHarness = m.spawnHarnesses[0]
	m.spawnModel = ""
	m.spawnModelPicker = nil
	m.syncSpawnModelWithHarness()
}

func (m hubModel) spawnHarnessKind() string {
	if kind := strings.TrimSpace(m.spawnHarnessKinds[m.spawnHarness]); kind != "" {
		return kind
	}
	return "serf"
}

func (m hubModel) spawnHarnessUsesSerfModels() bool {
	return m.spawnHarnessKind() != "codex"
}

func (m hubModel) spawnSelectableModels() []modelPickerItem {
	if !m.spawnHarnessUsesSerfModels() {
		return m.spawnHarnessModels[m.spawnHarness]
	}
	return m.spawnModels
}

func (m *hubModel) syncSpawnModelWithHarness() {
	if !m.spawnHarnessUsesSerfModels() {
		if strings.Contains(strings.TrimSpace(m.spawnModel), "/") {
			m.spawnModel = ""
		}
		m.spawnModelPicker = nil
		return
	}
	if strings.TrimSpace(m.spawnModel) == "" {
		models := m.spawnSelectableModels()
		if model, ok := firstEnabledModel(models); ok {
			m.spawnModel = model.id
		}
	}
}

func firstEnabledModel(models []modelPickerItem) (modelPickerItem, bool) {
	for _, model := range models {
		if strings.TrimSpace(model.disabledReason) == "" {
			return model, true
		}
	}
	return modelPickerItem{}, false
}

func (m hubModel) spawnModelDisabledReason(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	for _, item := range m.spawnSelectableModels() {
		if strings.TrimSpace(item.id) == model || strings.TrimSpace(item.display) == model {
			return strings.TrimSpace(item.disabledReason)
		}
	}
	return ""
}

func (m hubModel) spawnEmptyTaskUnsupportedReason() string {
	if m.spawnEmptyTaskReasons == nil {
		return ""
	}
	return strings.TrimSpace(m.spawnEmptyTaskReasons[m.spawnHarness])
}

func (m hubModel) spawnEmptyTaskUnsupportedNextAction() string {
	if m.spawnEmptyTaskNext == nil {
		return ""
	}
	return strings.TrimSpace(m.spawnEmptyTaskNext[m.spawnHarness])
}

func (m *hubModel) openSpawnModelPicker(models []modelPickerItem) {
	picker := newModelPicker(models, m.spawnModel, m.width)
	picker.title = m.spawnModelPickerTitle()
	m.spawnModelPicker = &picker
	m.err = nil
}

func (m hubModel) spawnModelPickerTitle() string {
	if m.spawnHarnessUsesSerfModels() {
		return "Select spawn model"
	}
	return "Select " + m.spawnHarness + " model"
}

func (m hubModel) spawnHarnessModelDisplay() string {
	model := strings.TrimSpace(m.spawnModel)
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return m.spawnHarness + "/" + model
}

func (m hubModel) spawnDirView() string {
	if m.spawnFocus == hubSpawnFieldDir {
		return m.spawnDirInput.View()
	}
	if dir := strings.TrimSpace(m.spawnDir); dir != "" {
		return dir
	}
	return "(hub default)"
}

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func (m hubModel) spawnWorkingDir() string {
	row, ok := m.selectedDashboardRow()
	if !ok {
		return ""
	}
	return m.workingDirForProjectKey(row.projectKey)
}

func (m hubModel) spawnProjectName() string {
	row, ok := m.selectedDashboardRow()
	if !ok || row.kind == hubRowLaunch {
		return ""
	}
	return row.project
}

func (m hubModel) selectedDashboardRow() (hubRow, bool) {
	rows := m.dashboardRows()
	if len(rows) == 0 || m.selected < 0 || m.selected >= len(rows) {
		return hubRow{}, false
	}
	return rows[m.selected], true
}

func (m hubModel) workingDirForProjectKey(projectKey string) string {
	if projectKey == "" {
		return ""
	}
	for _, p := range m.tree.Projects {
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		if key == projectKey {
			return p.WorkingDir
		}
	}
	return ""
}

func (m hubModel) projectKeyForSession() (string, bool) {
	project := strings.TrimSpace(m.detail.Project)
	if project == "" || project == "." {
		if m.detail.WorkingDir != "" {
			parts := strings.Split(strings.TrimRight(m.detail.WorkingDir, "/"), "/")
			project = parts[len(parts)-1]
		}
	}
	if project == "" {
		return "", false
	}
	want := hubProjectKey(project)
	for _, p := range m.tree.Projects {
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		if key == want || p.Name == project {
			return key, true
		}
	}
	return want, true
}

func (m hubModel) lastBrowseMessageIndex() int {
	for i := len(m.session.messages) - 1; i >= 0; i-- {
		if renderMessage(m.session.messages[i], max(m.width, 80), false) != "" {
			return i
		}
	}
	return -1
}

func (m *hubModel) moveBrowseSelection(delta int) {
	if len(m.session.messages) == 0 {
		m.browseSelected = -1
		return
	}
	idx := m.browseSelected
	if idx < 0 || idx >= len(m.session.messages) {
		idx = m.lastBrowseMessageIndex()
	}
	for {
		idx += delta
		if idx < 0 || idx >= len(m.session.messages) {
			break
		}
		if renderMessage(m.session.messages[idx], max(m.width, 80), false) != "" {
			m.browseSelected = idx
			m.session.scrollToMessage(idx)
			return
		}
	}
	m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
	if delta > 0 {
		m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

func (m *hubModel) moveBrowsePage(direction int) {
	step := m.session.viewport.Height
	if step < 1 {
		step = 5
	}
	for i := 0; i < step; i++ {
		before := m.browseSelected
		m.moveBrowseSelection(direction)
		if m.browseSelected == before {
			return
		}
	}
}

func (m hubModel) selectedBrowseMessage() (int, chatMessage, bool) {
	if m.browseSelected < 0 || m.browseSelected >= len(m.session.messages) {
		return -1, chatMessage{}, false
	}
	return m.browseSelected, m.session.messages[m.browseSelected], true
}

func (m *hubModel) startForkDraft() {
	_, msg, ok := m.selectedBrowseMessage()
	if !ok {
		m.addSessionSystem("Select a user turn to fork.")
		return
	}
	if msg.Kind != msgUser {
		m.addSessionSystem("Select a user turn to fork.")
		return
	}
	if msg.TurnIndex <= 0 {
		m.addSessionSystem("fork requires persisted transcript turn identity.")
		return
	}
	if !m.detail.Capabilities.Fork {
		m.addSessionSystem("Fork is not available for this session.")
		return
	}
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return
	}
	m.forkDraft = &hubForkDraft{
		Ref:          ref,
		Turn:         msg.TurnIndex,
		OriginalText: msg.Text,
		Label:        "original before fork",
	}
	m.exitSessionBrowse()
	m.session.setInputValue(msg.Text)
	m.addSessionSystem(fmt.Sprintf("Fork draft for turn %d. Edit the input, press enter to fork, or esc to cancel.", msg.TurnIndex))
}

func (m *hubModel) addSessionSystem(text string) {
	m.session.messages = append(m.session.messages, chatMessage{Kind: msgSystem, Text: text})
	m.session.refreshViewport()
}

func (m *hubModel) addAuthErrorNotice(title string, err error) {
	m.addNotice(noticePanel{
		Title:      title,
		Category:   "auth",
		Summary:    "OpenAI authentication failed.",
		Source:     m.sourceLabelForNotice(),
		Reason:     err.Error(),
		NextAction: "Retry /auth openai or check Hub auth configuration.",
	})
}

func (m *hubModel) recordSessionError(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.sessionStatusError = text
	m.addSessionSystem(text)
}

func (m *hubModel) clearSessionError() {
	m.sessionStatusError = ""
}

func (m *hubModel) removeTrailingSessionSystem(text string) {
	if len(m.session.messages) == 0 {
		return
	}
	last := m.session.messages[len(m.session.messages)-1]
	if last.Kind != msgSystem || last.Text != text {
		return
	}
	m.session.messages = m.session.messages[:len(m.session.messages)-1]
	m.session.refreshViewport()
}

func (m *hubModel) addSessionSystemOnce(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(m.session.messages) > 0 {
		last := m.session.messages[len(m.session.messages)-1]
		if last.Kind == msgSystem && last.Text == text {
			return
		}
	}
	m.addSessionSystem(text)
}

func (m hubModel) currentRef() (appwire.Ref, bool) {
	ref, err := appwire.ParseRef(m.detail.Ref)
	if err != nil {
		return appwire.Ref{}, false
	}
	return ref, true
}

func (m hubModel) matchesAsyncSessionRef(ref string) bool {
	return m.mode == hubModeSession && strings.TrimSpace(ref) != "" && strings.TrimSpace(m.detail.Ref) == strings.TrimSpace(ref)
}

func (m *hubModel) applyHubNotification(notification appwire.Notification) tea.Cmd {
	// Panel-refresh notifications fire regardless of current mode.
	switch notification.Method {
	case appwire.NotifySerfAuthUpdated:
		if m.credentialsPanel != nil && m.client != nil {
			return cmdAuthList(m.client)
		}
		return nil
	case appwire.NotifySerfLaunchUpdated:
		if m.launchSettingsPanel != nil {
			return m.launchSettingsPanel.initialCmd()
		}
		return nil
	}

	if m.mode != hubModeSession {
		return nil
	}
	if !m.notificationMatchesCurrentSession(notification) {
		return nil
	}
	var cmd tea.Cmd
	switch notification.Method {
	case appwire.NotifyTurnStarted:
		var params struct {
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.setActiveTurnID(params.Turn.ID)
		}
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			previous := m.detail.State
			m.detail.State = params.Status.Type
			m.session.processing = params.Status.Type == appwire.ThreadStatusActive
			// Refresh on any transition so capabilities (interrupt, steer, send, etc.)
			// reflect the source's current view. Without this, the cached idle snapshot
			// keeps Interrupt=false for the entire turn (kata 4yvd).
			if previous != params.Status.Type && m.client != nil {
				if ref, ok := m.currentRef(); ok {
					m.statusRefreshToken++
					cmd = fetchHubSessionExpectingStateToken(m.client, ref, params.Status.Type, m.statusRefreshToken)
				}
			}
		}
	case appwire.NotifyItemStarted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyThreadItem(params.Item, false)
		}
	case appwire.NotifyItemCompleted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyThreadItem(params.Item, true)
		}
	case appwire.NotifyAgentMessageDelta:
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			m.applyAgentMessageDelta(params.TurnID, params.ItemID, params.Delta)
		}
	case appwire.NotifyToolOutputDelta:
		var params appwire.ToolOutputDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.applyToolOutputDelta(params.ItemID, params.Delta)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyTurnCompleted:
		var params struct {
			// serf:naming-ignore: AppWire envelope field
			TurnID string       `json:"turnId"`
			Turn   appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			turnID := firstNonEmptyString(params.TurnID, params.Turn.ID)
			for _, item := range params.Turn.Items {
				if item.TurnID == "" {
					item.TurnID = turnID
				}
				m.applyThreadItem(item, true)
			}
			if turnID != "" && turnID == m.detail.ActiveTurnID {
				m.detail.ActiveTurnID = ""
			}
			if params.Turn.Status == appwire.TurnStatusFailed {
				m.addSessionSystemOnce(formatHubTurnError(params.Turn.Error, "Session error"))
			}
			// Queue head pop is now driven by thread/queueChanged from
			// the daemon (kata r80p); we no longer mirror locally on turn
			// completion.
		}
	case appwire.NotifyThreadQueueChanged:
		var params appwire.ThreadQueueChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			ref := strings.TrimSpace(params.Ref)
			if ref == "" {
				ref = strings.TrimSpace(m.detail.Ref)
			}
			m.applyQueueState(ref, params.Queue)
		}
	case appwire.NotifySerfSteeringInjected:
		var params struct {
			Text   string              `json:"text"`
			Images []appwire.InputItem `json:"images"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			text := strings.TrimSpace(params.Text)
			if text == "" {
				text = imageItemsPlaceholder(params.Images)
			}
			if text != "" {
				m.session.messages = append(m.session.messages, chatMessage{Kind: msgSteering, Text: text})
			}
		}
	case appwire.NotifyWarning:
		// Cause is decoded as a pointer so its absence (legacy payloads)
		// stays distinguishable from kind=="" (kata 5q3p). When present,
		// classifyWarningCategory uses the typed Cause; otherwise it falls
		// back to the message-substring path so legacy NotifyWarning
		// payloads still classify correctly.
		var params struct {
			Message string                   `json:"message"`
			Source  string                   `json:"source"`
			Title   string                   `json:"title"`
			Cause   *appwire.DiagnosticCause `json:"cause"`
			Warning struct {
				Message string `json:"message"`
			} `json:"warning"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			message := params.Message
			if strings.TrimSpace(message) == "" {
				message = params.Warning.Message
			}
			title := params.Title
			source := params.Source
			if strings.TrimSpace(title) == "" && strings.TrimSpace(source) == "" && classifyWarningCategory(message, params.Cause) == "provider" {
				source = "provider"
			}
			m.addSessionSystemOnce(formatHubDiagnosticWithCause(title, source, message, "Session warning", params.Cause))
		}
	}
	m.session.refreshViewport()
	// After the authoritative reducer update has applied, reconcile
	// any matching pending optimistic placeholder. This is the SINGLE
	// reconciliation site on the TUI side per the spec.
	if m.pending != nil {
		reconcilePendingFromNotification(m.pending, notification)
	}
	return cmd
}

// reconcilePendingFromNotification translates an inbound daemon
// notification into the wire-method name(s) the pending coordinator
// registered under, then calls TryReconcile. Some notifications
// match multiple methods (serf/steering/injected reconciles both
// turn/steer with matching text AND any in-flight turn/drainAsSteer).
//
// Drain-special: turn/drainAsSteer matches first-come-first-served
// regardless of text, because the daemon collapses queued entries
// into one STEERING and the placeholder doesn't know the joined text.
func reconcilePendingFromNotification(pending *pendingCoordinator, n appwire.Notification) {
	ref := notificationPendingRef(n)
	switch n.Method {
	case appwire.NotifySerfSteeringInjected:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(n.Params, &p)
		pending.TryReconcile(appwire.MethodTurnSteer, p.Text, ref)
		pending.TryReconcile(appwire.MethodTurnDrainAsSteer, "", ref)
	case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
		// userMessage item carries the user's text. Match against
		// any turn/start pending entry.
		var p struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return
		}
		if p.Item.Type == "userMessage" && (p.Item.Text != "" || len(p.Item.Images) > 0) {
			text := p.Item.Text
			if text == "" {
				text = imageItemsPlaceholder(p.Item.Images)
			}
			pending.TryReconcile(appwire.MethodTurnStart, text, ref)
		}
	case appwire.NotifyTurnCompleted:
		var p struct {
			Turn appwire.Turn `json:"turn"`
		}
		if err := json.Unmarshal(n.Params, &p); err != nil {
			return
		}
		for _, item := range p.Turn.Items {
			if item.Type != "userMessage" || (item.Text == "" && len(item.Images) == 0) {
				continue
			}
			text := item.Text
			if text == "" {
				text = imageItemsPlaceholder(item.Images)
			}
			pending.TryReconcile(appwire.MethodTurnStart, text, ref)
		}
	}
}

func notificationPendingRef(n appwire.Notification) string {
	var p appwire.NotificationRef
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return ""
	}
	if strings.TrimSpace(p.Ref) != "" {
		return strings.TrimSpace(p.Ref)
	}
	return strings.TrimSpace(p.ThreadID)
}

// markPendingFailedByID flips the chatMessage with the given PendingID
// from Pending → Failed and stamps the reason. ID-keyed so simultaneous
// placeholders of the same kind (e.g. a steer and a drain both rendered
// as msgSteering) can't cross-fail each other.
func (m *hubModel) markPendingFailedByID(id int64, reason string) {
	for i := range m.session.messages {
		if m.session.messages[i].PendingID != id {
			continue
		}
		m.session.messages[i].Pending = false
		m.session.messages[i].Failed = true
		m.session.messages[i].Reason = reason
		return
	}
}

// removePendingByID drops the chatMessage with the given PendingID
// after the authoritative event has rendered separately.
func (m *hubModel) removePendingByID(id int64) {
	for i := range m.session.messages {
		if m.session.messages[i].PendingID != id {
			continue
		}
		m.session.messages = append(m.session.messages[:i], m.session.messages[i+1:]...)
		return
	}
}

func (m *hubModel) setActiveTurnID(turnID string) {
	m.detail.ActiveTurnID = turnID
}

// applyQueueState replaces the local preview with the authoritative
// wire-sourced snapshot (kata r80p). Called from ReadThread responses and
// from thread/queueChanged notifications. Scoped to the current session
// ref so a notification routed to a different session can't leak into
// this view.
func (m *hubModel) applyQueueState(ref string, queue appwire.QueueState) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	m.sessionQueueRef = ref
	if queue.Depth == 0 && len(queue.Preview) == 0 {
		m.sessionQueue = nil
		return
	}
	m.sessionQueue = append([]string(nil), queue.Preview...)
}

// clearSessionQueue empties the local queue preview. Called when
// navigating away from a session so a stale preview never bleeds across
// views; new state arrives via the next ReadThread / queueChanged.
func (m *hubModel) clearSessionQueue() {
	m.sessionQueue = nil
	m.sessionQueueRef = ""
}

func (m hubModel) notificationMatchesCurrentSession(notification appwire.Notification) bool {
	var params appwire.NotificationRef
	if json.Unmarshal(notification.Params, &params) != nil {
		return true
	}

	detailRef := strings.TrimSpace(m.detail.Ref)
	if params.Ref != "" && detailRef != "" {
		return params.Ref == detailRef
	}

	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return true
	}
	if threadID == strings.TrimSpace(m.detail.SessionID) {
		return true
	}
	if ref, err := appwire.ParseRef(detailRef); err == nil && ref.ThreadID != "" {
		return threadID == ref.ThreadID
	}
	return false
}

func (m *hubModel) applyAgentMessageDelta(turnID, itemID, delta string) {
	reducer := m.sessionTranscriptReducer()
	reducer.applyAgentMessageDelta(turnID, itemID, delta)
	m.applySessionTranscriptReducer(reducer)
}

func (m *hubModel) applyThreadItem(item appwire.ThreadItem, completed bool) {
	reducer := m.sessionTranscriptReducer()
	reducer.applyThreadItem(item, turnIndexFromID(item.TurnID), completed)
	m.applySessionTranscriptReducer(reducer)
}

func (m *hubModel) sessionTranscriptReducer() hubTranscriptReducer {
	return newHubTranscriptReducer(m.session.messages, m.session.activeTools, m.session.activeMessages)
}

func (m *hubModel) applySessionTranscriptReducer(reducer hubTranscriptReducer) {
	m.session.messages = reducer.messages
	m.session.activeTools = reducer.activeTools
	m.session.activeMessages = reducer.activeMessages
}

func (m *hubModel) replaceSessionTranscript(messages []chatMessage) {
	m.session.messages = append([]chatMessage(nil), messages...)
	m.session.activeTools = nil
	m.session.activeMessages = nil
	m.browseSelected = -1
	m.transcriptView = nil
	m.session.refreshViewport()
}

func buildHubRows(tree hubTreeResponse) []hubRow {
	return buildDashboardRows(tree)
}

func buildDashboardRows(tree hubTreeResponse) []hubRow {
	type dashboardGroup struct {
		key       string
		name      string
		state     string
		updatedAt int64
		order     int
		sessions  []hubRow
	}

	seen := map[string]bool{}
	groups := map[string]*dashboardGroup{}
	var projectOrder []string

	ensureGroup := func(key, name, state string) *dashboardGroup {
		if name == "" {
			name = "(no project)"
		}
		if key == "" {
			key = hubProjectKey(name)
		}
		if group, ok := groups[key]; ok {
			if group.name == "" || group.name == "(no project)" {
				group.name = name
			}
			return group
		}
		group := &dashboardGroup{key: key, name: name, state: state, order: len(projectOrder)}
		groups[key] = group
		projectOrder = append(projectOrder, key)
		return group
	}
	addSession := func(key, project string, n hubTreeNode) {
		ref, err := appwire.ParseRef(n.Ref)
		if err != nil || seen[n.Ref] {
			return
		}
		seen[n.Ref] = true
		if project == "" {
			project = n.Project
		}
		if project == "" {
			project = "(no project)"
		}
		if key == "" {
			key = hubProjectKey(project)
		}
		title := n.Title
		if title == "" {
			title = n.SessionID
		}
		rowID := n.RowID
		if rowID == "" {
			rowID = "project:" + key + ":" + n.Ref
		}
		sourceLabel := strings.TrimSpace(n.SourceLabel)
		if sourceLabel == "" {
			sourceLabel = sourceLabelFromRef(ref)
		}
		row := hubRow{
			kind:        hubRowSession,
			ref:         ref,
			sourceLabel: sourceLabel,
			title:       title,
			project:     project,
			projectKey:  key,
			state:       n.State,
			live:        n.Live,
			model:       n.Model,
			age:         n.Age,
			rowID:       rowID,
			createdAt:   n.CreatedAt,
			updatedAt:   n.UpdatedAt,
		}
		group := ensureGroup(key, project, n.State)
		group.sessions = append(group.sessions, row)
		if attentionRankLabel(n.State) > attentionRankLabel(group.state) {
			group.state = stateLabel(n.State)
		}
		if recency := rowRecency(row); recency > group.updatedAt {
			group.updatedAt = recency
		}
	}

	for _, p := range tree.Projects {
		if len(p.Sessions) == 0 {
			continue
		}
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		ensureGroup(key, p.Name, p.RollupState)
		for _, n := range p.Sessions {
			addSession(key, p.Name, n)
			for _, child := range n.Children {
				addSession(key, p.Name, child)
			}
		}
	}

	for _, n := range tree.Live {
		if seen[n.Ref] {
			continue
		}
		project := n.Project
		if project == "" {
			project = "(no project)"
		}
		key := hubProjectKey(project)
		addSession(key, project, n)
	}

	ordered := make([]*dashboardGroup, 0, len(projectOrder))
	for _, key := range projectOrder {
		group := groups[key]
		if group == nil || len(group.sessions) == 0 {
			continue
		}
		sort.SliceStable(group.sessions, func(i, j int) bool {
			return dashboardRowLess(group.sessions[i], group.sessions[j])
		})
		ordered = append(ordered, group)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := hubRow{state: ordered[i].state, updatedAt: ordered[i].updatedAt}
		right := hubRow{state: ordered[j].state, updatedAt: ordered[j].updatedAt}
		if dashboardRowLess(left, right) {
			return true
		}
		if dashboardRowLess(right, left) {
			return false
		}
		return ordered[i].order < ordered[j].order
	})

	rows := make([]hubRow, 0, len(seen)+len(ordered))
	for _, group := range ordered {
		liveCount, recentCount := 0, 0
		for _, row := range group.sessions {
			if row.live && stateLabel(row.state) != "ended" {
				liveCount++
			} else {
				recentCount++
			}
		}
		rows = append(rows, hubRow{
			kind:        hubRowProject,
			title:       group.name,
			project:     group.name,
			projectKey:  group.key,
			state:       group.state,
			live:        true,
			rowID:       "project:" + group.key,
			updatedAt:   group.updatedAt,
			liveCount:   liveCount,
			recentCount: recentCount,
		})
		rows = append(rows, group.sessions...)
	}
	return rows
}

func dashboardRowLess(a, b hubRow) bool {
	ar, br := attentionRankLabel(a.state), attentionRankLabel(b.state)
	if ar != br {
		return ar > br
	}
	au, bu := rowRecency(a), rowRecency(b)
	if au != bu {
		return au > bu
	}
	if a.project != b.project {
		return strings.ToLower(a.project) < strings.ToLower(b.project)
	}
	return strings.ToLower(a.title) < strings.ToLower(b.title)
}

func rowRecency(row hubRow) int64 {
	if row.updatedAt > 0 {
		return row.updatedAt
	}
	return row.createdAt
}

func buildProjectRows(project hubTreeProject) []hubRow {
	var liveRows []hubRow
	var recentRows []hubRow
	key := project.Key
	if key == "" {
		key = hubProjectKey(project.Name)
	}
	add := func(n hubTreeNode) {
		ref, err := appwire.ParseRef(n.Ref)
		if err != nil {
			return
		}
		title := n.Title
		if title == "" {
			title = n.SessionID
		}
		state := n.State
		if state == "" && !n.Live {
			state = "ended"
		}
		rowID := n.RowID
		if rowID == "" {
			rowID = "project:" + key + ":" + n.Ref
		}
		sourceLabel := strings.TrimSpace(n.SourceLabel)
		if sourceLabel == "" {
			sourceLabel = sourceLabelFromRef(ref)
		}
		row := hubRow{
			kind:        hubRowSession,
			ref:         ref,
			sourceLabel: sourceLabel,
			title:       title,
			project:     project.Name,
			projectKey:  key,
			state:       state,
			live:        n.Live,
			model:       n.Model,
			age:         n.Age,
			rowID:       rowID,
			createdAt:   n.CreatedAt,
			updatedAt:   n.UpdatedAt,
		}
		if n.Live {
			liveRows = append(liveRows, row)
		} else {
			recentRows = append(recentRows, row)
		}
	}
	for _, n := range project.Sessions {
		add(n)
		for _, child := range n.Children {
			add(child)
		}
	}
	rows := make([]hubRow, 0, len(liveRows)+len(recentRows))
	rows = append(rows, liveRows...)
	rows = append(rows, recentRows...)
	return rows
}

func hubProjectKey(name string) string {
	if name == "" {
		return "project"
	}
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(name)
}

func (m hubModel) dashboardRows() []hubRow {
	if strings.TrimSpace(m.dashboardFilter.Value()) != "" {
		return filterHubRows(m.rows, m.dashboardFilter.Value())
	}
	return m.foldedDashboardRows()
}

func (m hubModel) foldedDashboardRows() []hubRow {
	rows := []hubRow{{
		kind:  hubRowLaunch,
		title: "Launch New Session",
		rowID: "dashboard:launch",
	}}
	for i := 0; i < len(m.rows); {
		project := m.rows[i]
		if project.kind != hubRowProject {
			i++
			continue
		}
		rows = append(rows, project)
		i++
		if !m.dashboardProjectExpanded(project.projectKey) {
			for i < len(m.rows) && m.rows[i].kind != hubRowProject {
				i++
			}
			continue
		}

		recent := make([]hubRow, 0, project.recentCount)
		for i < len(m.rows) && m.rows[i].kind != hubRowProject {
			row := m.rows[i]
			if row.kind == hubRowSession {
				if row.live && stateLabel(row.state) != "ended" {
					rows = append(rows, row)
				} else {
					recent = append(recent, row)
				}
			}
			i++
		}
		if len(recent) == 0 {
			continue
		}
		rows = append(rows, hubRow{
			kind:        hubRowRecentToggle,
			title:       "Ended Sessions",
			project:     project.project,
			projectKey:  project.projectKey,
			state:       "ended",
			rowID:       "project:" + project.projectKey + ":recent",
			recentCount: len(recent),
		})
		if m.dashboardRecentOpen[project.projectKey] {
			rows = append(rows, recent...)
		}
	}
	return rows
}

func (m hubModel) dashboardProjectExpanded(projectKey string) bool {
	return !m.dashboardProjectClosed[projectKey]
}

func (m *hubModel) setSelectedDashboardProjectExpanded(rows []hubRow, expanded bool) {
	if len(rows) == 0 || m.selected < 0 || m.selected >= len(rows) {
		return
	}
	row := rows[m.selected]
	if row.kind != hubRowProject {
		return
	}
	m.setDashboardProjectExpanded(row.projectKey, expanded)
	m.clampSelection()
}

func (m *hubModel) toggleDashboardProject(projectKey string) {
	m.setDashboardProjectExpanded(projectKey, !m.dashboardProjectExpanded(projectKey))
}

func (m *hubModel) setDashboardProjectExpanded(projectKey string, expanded bool) {
	if projectKey == "" {
		return
	}
	if m.dashboardProjectClosed == nil {
		m.dashboardProjectClosed = map[string]bool{}
	}
	if expanded {
		delete(m.dashboardProjectClosed, projectKey)
		return
	}
	m.dashboardProjectClosed[projectKey] = true
}

func (m *hubModel) toggleDashboardRecent(projectKey string) {
	if projectKey == "" {
		return
	}
	if m.dashboardRecentOpen == nil {
		m.dashboardRecentOpen = map[string]bool{}
	}
	if m.dashboardRecentOpen[projectKey] {
		delete(m.dashboardRecentOpen, projectKey)
		return
	}
	m.dashboardRecentOpen[projectKey] = true
}

func (m *hubModel) focusDashboardProject(projectKey string) {
	if projectKey == "" {
		m.returnToDashboard()
		return
	}
	m.mode = hubModeDashboard
	m.dashboardFilter.Reset()
	m.dashboardFilter.Blur()
	m.dashboardFilterActive = false
	m.setDashboardProjectExpanded(projectKey, true)
	rows := m.dashboardRows()
	for i, row := range rows {
		if row.kind == hubRowProject && row.projectKey == projectKey {
			m.selected = i
			return
		}
	}
	m.clampSelection()
}

func (m hubModel) sessionSearchRows() []hubRow {
	key, ok := m.projectKeyForSession()
	if ok {
		for _, project := range m.tree.Projects {
			projectKey := project.Key
			if projectKey == "" {
				projectKey = hubProjectKey(project.Name)
			}
			if projectKey == key {
				return buildProjectRows(project)
			}
		}
	}
	return m.dashboardRows()
}

func filterHubRows(rows []hubRow, query string) []hubRow {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return rows
	}
	projectMatches := map[string]bool{}
	childMatches := map[string]bool{}
	for _, row := range rows {
		if rowMatchesFilter(row, query) {
			if row.kind == hubRowProject {
				projectMatches[row.projectKey] = true
			} else {
				childMatches[row.projectKey] = true
			}
		}
	}
	filtered := make([]hubRow, 0, len(rows))
	for _, row := range rows {
		if row.kind == hubRowProject {
			if projectMatches[row.projectKey] || childMatches[row.projectKey] {
				filtered = append(filtered, row)
			}
			continue
		}
		if projectMatches[row.projectKey] || rowMatchesFilter(row, query) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func rowMatchesFilter(row hubRow, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		row.title,
		row.project,
		row.projectKey,
		row.sourceLabel,
		row.model,
		row.state,
		row.age,
	}, " "))
	return strings.Contains(haystack, query)
}

func (m *hubModel) clampSelection() {
	n := len(m.dashboardRows())
	if n == 0 {
		m.selected = 0
		return
	}
	if m.selected >= n {
		m.selected = n - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
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

func (m hubModel) dashboardView() string {
	rows := m.dashboardRows()
	liveCount := 0
	for _, row := range m.rows {
		if row.kind == hubRowSession && row.live {
			liveCount++
		}
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	topBar := dashboardHeader(m.hubURL, liveCount, width)
	var b strings.Builder
	if m.err != nil {
		b.WriteString(truncateText(fmt.Sprintf("error: %v", m.err), width))
		b.WriteString("\n\n")
	}
	if m.commandPalette != nil {
		return appShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.commandPalette.View(),
			Footer:  actionBarForWidth(m.width, "up/down select", "enter open/toggle", "n new", "/ palette", "ctrl+o dashboard", "q quit"),
			Height:  m.height,
		}.View()
	}
	if m.followupModal != nil {
		return appShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.followupModal.View(),
			Footer:  "[Enter] confirm  [Esc] cancel",
			Height:  m.height,
		}.View()
	}
	if m.credentialsPanel != nil {
		return appShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.credentialsPanel.View(),
			Footer:  "[Enter] set api key  [O] OAuth sign-in  [C] clear  [Esc] close",
			Height:  m.height,
		}.View()
	}
	if m.launchSettingsPanel != nil {
		return appShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.launchSettingsPanel.View(),
			Footer:  "[←/→] tab  [↑/↓] field  [Enter] edit  [Esc] close",
			Height:  m.height,
		}.View()
	}
	if m.dashboardFilterActive || strings.TrimSpace(m.dashboardFilter.Value()) != "" {
		b.WriteString(m.dashboardFilter.View())
		b.WriteString("\n\n")
	}
	if len(rows) == 0 {
		if strings.TrimSpace(m.dashboardFilter.Value()) != "" {
			b.WriteString("No sessions match this filter.\n\n")
			return appShell{
				TopBar: topBar,
				Body:   b.String(),
				Footer: "esc clear filter",
				Height: m.height,
			}.View()
		}
		b.WriteString("No live sessions are running.\n\n")
		return appShell{
			TopBar: topBar,
			Body:   b.String(),
			Footer: emptyDashboardFooter(width),
			Height: m.height,
		}.View()
	}
	footer := dashboardFooter(width)
	rowLimit := dashboardRowLimit(m.height, topBar, b.String(), footer)
	if m.dashboardUsesWideLayout() {
		drawerWidth := min(72, max(42, width/2))
		listWidth := max(40, width-drawerWidth-2)
		list := renderDashboardRowsWindow(rows, m.selected, listWidth, false, rowLimit)
		drawer := limitFirstLines(m.dashboardDetailsView(rows, drawerWidth), rowLimit)
		b.WriteString(joinDashboardColumns(list, drawer, listWidth, drawerWidth, width))
	} else {
		b.WriteString(renderDashboardRowsWindow(rows, m.selected, width, width <= 72, rowLimit))
	}
	b.WriteString("\n")
	return appShell{
		TopBar: topBar,
		Body:   b.String(),
		Footer: footer,
		Height: m.height,
	}.View()
}

func (m hubModel) dashboardUsesWideLayout() bool {
	return m.width >= 120 && m.commandPalette == nil
}

func dashboardHeader(hubURL string, liveCount int, width int) string {
	left := "serf live"
	right := fmt.Sprintf("%s · %d live", hubURL, liveCount)
	if width <= 0 {
		return left + "  " + right
	}
	gap := width - len([]rune(left)) - len([]rune(right))
	if gap < 2 {
		return truncateText(left+"  "+right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func renderDashboardRows(rows []hubRow, selected int, width int, compact bool) string {
	return renderDashboardRowsWindow(rows, selected, width, compact, 0)
}

func renderDashboardRowsWindow(rows []hubRow, selected int, width int, compact bool, maxRows int) string {
	var b strings.Builder
	start, end := dashboardRowWindow(len(rows), selected, maxRows)
	for i := start; i < end; i++ {
		row := rows[i]
		switch row.kind {
		case hubRowLaunch:
			b.WriteString(renderDashboardLaunchRow(row, i == selected, width))
			b.WriteString("\n")
			continue
		case hubRowProject:
			b.WriteString(renderDashboardProjectRow(row, rows, i == selected, width, dashboardProjectExpanded(rows, i)))
			b.WriteString("\n")
			continue
		case hubRowRecentToggle:
			b.WriteString(renderDashboardRecentToggleRow(row, dashboardRecentExpanded(rows, i), i == selected, width))
			b.WriteString("\n")
			continue
		}
		b.WriteString(renderDashboardSessionRow(row, i == selected, width, compact, dashboardSessionBranch(rows, i)))
		b.WriteString("\n")
	}
	return b.String()
}

func dashboardRowWindow(count int, selected int, maxRows int) (int, int) {
	if count <= 0 {
		return 0, 0
	}
	if maxRows <= 0 || maxRows >= count {
		return 0, count
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= count {
		selected = count - 1
	}
	start := selected - maxRows/2
	if start < 0 {
		start = 0
	}
	if start+maxRows > count {
		start = count - maxRows
	}
	return start, start + maxRows
}

func renderDashboardLaunchRow(row hubRow, selected bool, width int) string {
	cursor := " "
	if selected {
		cursor = ">"
	}
	line := truncateText(cursor+" + "+row.title, width)
	if selected {
		return defaultTUIStyles().Selected.Render(line)
	}
	return line
}

func dashboardRowLimit(totalHeight int, topBar string, bodyPrefix string, footer string) int {
	if totalHeight <= 0 {
		return 0
	}
	limit := sessionShellBodyHeight(totalHeight, topBar, "", footer)
	limit -= shellSectionLineCount(bodyPrefix)
	if limit < 1 {
		return 1
	}
	return limit
}

func renderDashboardProjectRow(row hubRow, rows []hubRow, selected bool, width int, expanded bool) string {
	marker := "▾"
	if !expanded {
		marker = "▸"
	}
	cursor := " "
	if selected {
		cursor = ">"
	}
	styles := defaultTUIStyles()
	line := fmt.Sprintf("%s %s %s %s  %s", cursor, marker, statusDot(row.state), dashboardCell(row.project), projectSummary(row, rows))
	line = truncateText(line, width)
	if selected {
		return styles.Selected.Render(line)
	}
	return styles.Section.Render(line)
}

func renderDashboardRecentToggleRow(row hubRow, expanded bool, selected bool, width int) string {
	marker := "▸"
	if expanded {
		marker = "▾"
	}
	cursor := " "
	if selected {
		cursor = ">"
	}
	count := row.recentCount
	if count == 0 {
		count = 1
	}
	label := "recent"
	line := truncateText(fmt.Sprintf("%s %s %s %d %s", cursor, marker, dashboardCell(row.project), count, label), width)
	if selected {
		return defaultTUIStyles().Selected.Render(line)
	}
	return defaultTUIStyles().Muted.Render(line)
}

func renderDashboardSessionRow(row hubRow, selected bool, width int, compact bool, branch string) string {
	marker := branch
	if selected {
		marker = ">"
	}
	styles := defaultTUIStyles()
	if compact {
		line := strings.Join(nonEmptyStrings([]string{
			marker,
			statusDot(row.state),
			stateLabel(row.state),
			dashboardCell(row.sourceLabel),
			dashboardCell(row.project),
			dashboardTitle(row.title),
			dashboardCell(row.model),
			dashboardCell(row.age),
		}), " ")
		line = truncateText(line, width)
		if selected {
			return styles.Selected.Render(line)
		}
		return line
	}
	line := strings.Join(nonEmptyStrings([]string{
		marker,
		statusDot(row.state),
		stateLabel(row.state),
		dashboardCell(row.sourceLabel),
		dashboardCell(row.project),
		dashboardTitle(row.title),
		dashboardCell(row.model),
		dashboardCell(row.age),
	}), " ")
	line = truncateText(line, width)
	if selected {
		return styles.Selected.Render(line)
	}
	return line
}

func dashboardCell(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func dashboardTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = dashboardCell(line); line != "" {
			return line
		}
	}
	return dashboardCell(text)
}

func dashboardSessionBranch(rows []hubRow, index int) string {
	if index < 0 || index >= len(rows) || rows[index].kind != hubRowSession {
		return ""
	}
	projectKey := rows[index].projectKey
	for i := index + 1; i < len(rows); i++ {
		if rows[i].kind == hubRowProject {
			break
		}
		if rows[i].kind == hubRowSession && rows[i].projectKey == projectKey {
			return "├─"
		}
	}
	return "└─"
}

func dashboardRecentExpanded(rows []hubRow, index int) bool {
	if index < 0 || index >= len(rows) || rows[index].kind != hubRowRecentToggle {
		return false
	}
	projectKey := rows[index].projectKey
	for i := index + 1; i < len(rows); i++ {
		if rows[i].kind == hubRowProject || rows[i].kind == hubRowRecentToggle {
			return false
		}
		if rows[i].kind == hubRowSession && rows[i].projectKey == projectKey && (!rows[i].live || stateLabel(rows[i].state) == "ended") {
			return true
		}
	}
	return false
}

func dashboardProjectExpanded(rows []hubRow, index int) bool {
	if index < 0 || index >= len(rows) || rows[index].kind != hubRowProject {
		return false
	}
	projectKey := rows[index].projectKey
	for i := index + 1; i < len(rows); i++ {
		if rows[i].kind == hubRowProject {
			return false
		}
		if rows[i].projectKey == projectKey {
			return true
		}
	}
	return rows[index].liveCount == 0 && rows[index].recentCount == 0
}

func dashboardFooter(width int) string {
	if width <= 72 {
		return truncateText("keys: up/down enter n new / palette ctrl+o dashboard q", width)
	}
	return truncateText("up/down select  enter open/toggle  n new  / palette  ctrl+o dashboard  q quit", width)
}

func emptyDashboardFooter(width int) string {
	items := []string{"n new session"}
	items = append(items, "/ palette", "q quit")
	if width <= 72 {
		return strings.Join(items, "\n")
	}
	return strings.Join(items, "  ")
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

func (m hubModel) dashboardDetailsView(rows []hubRow, width int) string {
	if m.err != nil {
		return renderDetailsPane(strings.Join([]string{
			"details",
			"Diagnostic",
			"Message:  " + m.err.Error(),
			"Next:     refresh dashboard or check Hub health",
		}, "\n"), width)
	}
	if len(rows) == 0 || m.selected >= len(rows) {
		return renderDetailsPane("details\nNo dashboard row selected.", width)
	}
	row := rows[m.selected]
	switch row.kind {
	case hubRowLaunch:
		return renderDetailsPane("details\nAction:   enter launches an unscoped new session\nDir:      hub default", width)
	case hubRowProject:
		return renderDetailsPane(m.dashboardProjectDetails(row, rows), width)
	case hubRowRecentToggle:
		return renderDetailsPane(m.dashboardRecentDetails(row), width)
	case hubRowSession:
		return renderDetailsPane(dashboardSessionDetails(row), width)
	default:
		return renderDetailsPane("details\nNo dashboard row selected.", width)
	}
}

func renderDetailsPane(text string, width int) string {
	return renderStyledPane(text, width)
}

func (m hubModel) dashboardProjectDetails(row hubRow, rows []hubRow) string {
	var b strings.Builder
	b.WriteString("details\n")
	liveCount, recentCount := projectSessionCounts(row, rows)
	fmt.Fprintf(&b, "Project:  %s\n", row.project)
	fmt.Fprintf(&b, "Live:     %d\n", liveCount)
	fmt.Fprintf(&b, "Recent:   %d\n", recentCount)
	fmt.Fprintf(&b, "State:    %s\n", stateLabel(row.state))
	if dir := m.workingDirForProjectKey(row.projectKey); dir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", dir)
	}
	b.WriteString("Action:   enter toggles project")
	return b.String()
}

func (m hubModel) dashboardRecentDetails(row hubRow) string {
	var b strings.Builder
	b.WriteString("details\n")
	fmt.Fprintf(&b, "Project:  %s\n", row.project)
	fmt.Fprintf(&b, "Recent:   %d\n", row.recentCount)
	if dir := m.workingDirForProjectKey(row.projectKey); dir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", dir)
	}
	b.WriteString("Action:   enter toggles ended sessions")
	return b.String()
}

func dashboardSessionDetails(row hubRow) string {
	var b strings.Builder
	b.WriteString("details\n")
	sessionID := row.ref.ThreadID
	if sessionID == "" {
		sessionID = row.title
	}
	fmt.Fprintf(&b, "Session:  %s\n", sessionID)
	if row.title != "" {
		fmt.Fprintf(&b, "Title:    %s\n", row.title)
	}
	if ref := row.ref.String(); ref != ":" {
		fmt.Fprintf(&b, "Ref:      %s\n", ref)
	}
	if row.project != "" {
		fmt.Fprintf(&b, "Project:  %s\n", row.project)
	}
	if row.sourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", row.sourceLabel)
	}
	if row.state != "" {
		fmt.Fprintf(&b, "State:    %s\n", stateLabel(row.state))
	}
	if row.model != "" {
		fmt.Fprintf(&b, "Model:    %s\n", row.model)
	}
	if row.age != "" {
		fmt.Fprintf(&b, "Updated:  %s\n", row.age)
	}
	b.WriteString("Action:   enter opens session")
	return b.String()
}

func projectLiveCount(project hubRow, rows []hubRow) int {
	liveCount, _ := projectSessionCounts(project, rows)
	return liveCount
}

func projectSessionCounts(project hubRow, rows []hubRow) (int, int) {
	if project.kind == hubRowProject && (project.liveCount > 0 || project.recentCount > 0) {
		return project.liveCount, project.recentCount
	}
	count := 0
	recent := 0
	for _, row := range rows {
		if row.kind == hubRowSession && row.projectKey == project.projectKey {
			if row.live && stateLabel(row.state) != "ended" {
				count++
			} else {
				recent++
			}
		}
	}
	return count, recent
}

func truncateMultilineText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = truncateText(line, width)
	}
	return strings.Join(lines, "\n")
}

func statusDot(state string) string {
	switch stateLabel(state) {
	case "awaiting":
		return "●"
	case "active":
		return "●"
	case "warning":
		return "●"
	case "idle":
		return "●"
	default:
		return "○"
	}
}

func stateLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "awaiting":
		return "awaiting"
	case "active":
		return "active"
	case "warning":
		return "warning"
	case "idle":
		return "idle"
	case "notloaded":
		return "notLoaded"
	case "closed":
		return "ended"
	default:
		if strings.TrimSpace(state) == "" {
			return "notLoaded"
		}
		return state
	}
}

func projectSummary(project hubRow, rows []hubRow) string {
	liveCount, recentCount := projectSessionCounts(project, rows)
	attention := stateLabel(project.state)
	for _, row := range rows {
		if row.kind != hubRowSession || row.projectKey != project.projectKey {
			continue
		}
		if attentionRankLabel(row.state) > attentionRankLabel(attention) {
			attention = stateLabel(row.state)
		}
	}
	if recentCount > 0 {
		return fmt.Sprintf("%d live · %d recent · %s", liveCount, recentCount, attention)
	}
	return fmt.Sprintf("%d live · %s", liveCount, attention)
}

func attentionRankLabel(state string) int {
	switch stateLabel(state) {
	case "awaiting":
		return 4
	case "active":
		return 3
	case "warning":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

func (m hubModel) spawnView() string {
	var b strings.Builder
	topBar := "serf / new session"
	var overlay string
	if m.spawnModelPicker != nil {
		overlay = m.spawnModelPicker.View()
	}
	if m.launchOverridesModal != nil {
		overlay = m.launchOverridesModal.View()
	}
	if m.followupModal != nil {
		overlay = m.followupModal.View()
	}
	model := m.spawnModel
	models := m.spawnSelectableModels()
	if !m.spawnHarnessUsesSerfModels() {
		if model == "" {
			model = "(harness default)"
		} else {
			model = m.spawnHarnessModelDisplay()
		}
	} else if model == "" && len(models) == 0 {
		model = "(loading models...)"
	} else if model == "" {
		model = "(choose a model)"
	}
	fmt.Fprintf(&b, "%s Harness:  %s\n", m.spawnFieldPrefix(hubSpawnFieldHarness), m.spawnHarness)
	fmt.Fprintf(&b, "%s Model:    %s\n", m.spawnFieldPrefix(hubSpawnFieldModel), model)
	if m.spawnProject != "" {
		fmt.Fprintf(&b, "  Project:  %s\n", m.spawnProject)
	}
	fmt.Fprintf(&b, "%s Dir:      %s\n", m.spawnFieldPrefix(hubSpawnFieldDir), m.spawnDirView())
	fmt.Fprintf(&b, "%s Prompt (optional):\n", m.spawnFieldPrefix(hubSpawnFieldPrompt))
	for _, line := range strings.Split(strings.TrimSuffix(renderComposerDraft(m.session.input.Value()), "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if notices := m.renderNotices(); notices != "" {
		b.WriteString("\n")
		b.WriteString(notices)
	}
	if m.spawnSubmitting {
		b.WriteString("\nStarting session...\n")
	}

	var footer strings.Builder
	keys := []string{"tab: next field", "shift+tab: previous", m.spawnFieldHint(), "esc: cancel", "ctrl+o: dashboard"}
	footer.WriteString(actionBarForWidth(m.width, keys...))
	return appShell{
		TopBar:  topBar,
		Body:    b.String(),
		Overlay: overlay,
		Footer:  footer.String(),
		Height:  m.height,
	}.View()
}

func (m hubModel) sessionHeaderLines() []string {
	width := m.sessionHeaderWidth()
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	source := firstNonEmptyString(m.detail.SourceLabel, sourceLabelFromRefText(m.detail.Ref))
	state := strings.TrimSpace(m.detail.State)
	if state == "" {
		state = "unknown"
	} else {
		state = stateLabel(state)
	}
	model := sessionHeaderModelSummary(m.detail)
	project := firstNonEmptyString(m.detail.Project, "unknown")
	cwd := firstNonEmptyString(m.detail.WorkingDir, "unknown")
	ref := firstNonEmptyString(m.detail.Ref, "unknown")

	meta := fmt.Sprintf("source: %s  ref: %s  state: %s  %s", source, ref, state, model)
	location := fmt.Sprintf("project: %s  cwd: %s  turns: %d", project, cwd, m.detail.TurnCount)
	if m.detail.ContextPressure > 0 {
		location += fmt.Sprintf("  ctx: %.0f%%", m.detail.ContextPressure*100)
	}

	return []string{
		truncateSessionLine(title, width),
		truncateSessionLine(meta, width),
		truncateSessionLine(location, width),
		truncateSessionLine(m.sessionStatusLine(), width),
	}
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
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
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

func (m hubModel) sessionStatusErrorText() string {
	if m.err != nil {
		return m.err.Error()
	}
	return strings.TrimSpace(m.sessionStatusError)
}

func (m hubModel) sessionView() string {
	title := firstNonEmptyString(m.detail.Title, m.detail.SessionID, m.detail.Ref, "untitled session")
	topBar := truncateSessionLine(fmt.Sprintf("serf / session / %s", title), m.sessionHeaderWidth())
	var b strings.Builder
	for _, line := range m.sessionHeaderLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if notices := m.renderNotices(); notices != "" {
		b.WriteString("\n")
		b.WriteString(notices)
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
		for i, msg := range messages {
			focused := m.transcriptView == nil && (m.session.isToolFocused(i) || (m.session.scrollMode && m.browseSelected == i))
			rendered := renderMessage(msg, width, focused)
			if rendered == "" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(rendered)
			b.WriteString("\n")
		}
	}
	mainBody := b.String()
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
	var footer string
	switch {
	case m.transcriptView != nil:
		footer = actionBarForWidth(m.width, "esc/i/q: return to chat", "ctrl+o: dashboard")
	case m.session.scrollMode:
		keys := []string{"esc/i/q: compose"}
		if m.detail.Capabilities.Fork {
			keys = append(keys, "f: fork selected user turn")
		}
		keys = append(keys, "ctrl+o: dashboard")
		footer = actionBarForWidth(m.width, keys...)
	default:
		footer = m.sessionComposerPanel().View()
	}
	overlayText := overlay.String()
	bodyHeight := sessionShellBodyHeight(m.height, topBar, overlayText, footer)
	body := m.sessionBody(mainBody, bodyHeight, overlayText != "")
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

func (m hubModel) sessionBody(mainBody string, bodyHeight int, hasOverlay bool) string {
	if !hasOverlay && m.sessionPanel == nil {
		return mainBody
	}
	if bodyHeight <= 0 {
		return mainBody
	}
	return limitSessionBodyLines(mainBody, bodyHeight)
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
