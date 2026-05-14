package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type hubMode int

const (
	hubModeDashboard hubMode = iota
	hubModeProject
	hubModeSession
	hubModeSpawn
)

type hubSpawnField int

const (
	hubSpawnFieldPrompt hubSpawnField = iota
	hubSpawnFieldHarness
	hubSpawnFieldModel
)

type hubRowKind int

const (
	hubRowProject hubRowKind = iota
	hubRowSession
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

	dashboardFilter       textinput.Model
	dashboardFilterActive bool
	commandPalette        *commandPalette

	selectedProjectKey      string
	projectRows             []hubRow
	browseSelected          int
	forkDraft               *hubForkDraft
	sessionThemePicker      *themePicker
	sessionModelPicker      *modelPicker
	sessionTranscriptPicker *modelPicker
	sessionPanel            *hubSessionPanel
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
	spawnSubmitting         bool
	spawnFocus              hubSpawnField

	detail  hubSessionDetail
	session model
	notices []noticePanel

	authStatus         authStatus
	authStatusSeen     bool
	sessionStatusError string

	authLoginProvider string
	authLoginFlowID   string
}

func newHubModel(client *appwire.Client, hubURL string, stateDirs ...string) hubModel {
	stateDir := ""
	if len(stateDirs) > 0 {
		stateDir = strings.TrimSpace(stateDirs[0])
	}
	session := newModel("", "", nil)
	session.authController = nil
	return hubModel{client: client, hubURL: hubURL, stateDir: stateDir, session: session, browseSelected: -1, dashboardFilter: newHubFilterInput()}
}

func newHubFilterInput() textinput.Model {
	input := textinput.New()
	input.Prompt = "filter: "
	input.Placeholder = "title, project, model, source"
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
		if m.mode == hubModeProject {
			m.projectRows = m.rowsForSelectedProject()
		}
		m.clampSelection()
		return m, nil
	case hubSessionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.spawnSubmitting = false
		if m.mode == hubModeSession && m.detail.Ref == msg.detail.Ref {
			m.detail = msg.detail
			panel := hubSessionPanel{Body: m.renderSessionDetails()}
			m.sessionPanel = &panel
			m.session.refreshViewport()
			return m, nil
		}
		m.clearNoticesByCategory("action-unavailable")
		m.mode = hubModeSession
		m.detail = msg.detail
		m.session = newModel("", "", nil)
		m.session.width = m.width
		m.session.height = m.height
		m.session.sessionID = msg.detail.SessionID
		m.session.sessionModel = msg.detail.Model
		m.session.sessionProfile = msg.detail.Profile
		m.session.processing = msg.detail.State == "processing"
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
		m.transcriptTargets = nil
		m.transcriptView = nil
		return m, nil
	case hubNotificationMsg:
		if !msg.ok {
			return m, nil
		}
		cmd := m.applyHubNotification(msg.notification)
		return m, tea.Batch(cmd, waitHubNotification(m.client))
	case hubSendMsg:
		if msg.err != nil {
			m.session.setInputValue(msg.draft)
			m.addHubErrorNotice("Send failed", "appwire", msg.err, "Check the hub connection and retry the action.")
			m.recordSessionError("Send failed: " + msg.err.Error())
		} else {
			m.clearNoticesByCategory("appwire")
			m.clearSessionError()
			m.setActiveTurnID(msg.turnID)
		}
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
	case hubResumeMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("resume failed: %w", msg.err)
			return m, nil
		}
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.err = fmt.Errorf("resume returned invalid ref: %s", msg.resp.Ref)
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
	}
	return m, nil
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
	if m.mode == hubModeProject {
		return m.updateProjectKey(msg)
	}
	return m.updateDashboardKey(msg)
}

func (m hubModel) updateDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	case "p":
		if len(rows) > 0 {
			m.openProject(rows[m.selected].projectKey)
		} else if key := m.firstProjectHistoryKey(); key != "" {
			m.openProject(key)
		}
	case "enter":
		return m.activateDashboardRow(rows)
	}
	return m, nil
}

func (m hubModel) updateProjectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dashboardFilterActive {
		return m.updateHubFilterKey(msg)
	}
	rows := m.projectDisplayRows()
	switch msg.String() {
	case "/":
		m.openCommandPalette()
		return m, nil
	case "q":
		return m, tea.Quit
	case "esc", "backspace":
		m.mode = hubModeDashboard
		m.clampSelection()
	case "r":
		if len(rows) > 0 && m.client != nil {
			row := rows[m.selected]
			if !row.live {
				return m, sendHubResume(m.client, row.ref)
			}
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
	case "enter":
		return m.activateProjectRow(rows)
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
		if m.mode == hubModeProject {
			return m.activateProjectRow(m.projectDisplayRows())
		}
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
	if row.kind == hubRowProject {
		m.openProject(row.projectKey)
		return m, nil
	}
	if m.client == nil {
		return m, nil
	}
	return m, fetchHubSession(m.client, row.ref)
}

func (m hubModel) activateProjectRow(rows []hubRow) (tea.Model, tea.Cmd) {
	if len(rows) == 0 || m.client == nil {
		return m, nil
	}
	return m, fetchHubSession(m.client, rows[m.selected].ref)
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
	if m.mode == hubModeProject {
		rows = m.projectDisplayRows()
	} else if m.mode == hubModeSession {
		rows = m.sessionSearchRows()
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
		m.openProject(entry.ProjectKey)
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
		Prompt:     prompt,
		Harness:    strings.TrimSpace(m.spawnHarness),
		Model:      strings.TrimSpace(m.spawnModel),
		WorkingDir: m.spawnDir,
	}
	m.err = nil
	m.spawnSubmitting = true
	return m, sendHubSpawn(m.client, req)
}

func (m *hubModel) setSpawnFocus(field hubSpawnField) {
	if field < hubSpawnFieldPrompt || field > hubSpawnFieldModel {
		field = hubSpawnFieldPrompt
	}
	m.spawnFocus = field
	if field == hubSpawnFieldPrompt {
		m.session.input.Focus()
		return
	}
	m.session.input.Blur()
}

func (m *hubModel) advanceSpawnFocus(delta int) {
	next := int(m.spawnFocus) + delta
	count := int(hubSpawnFieldModel) + 1
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
	default:
		return "enter: spawn  ctrl+j: newline"
	}
}

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

	switch msg.String() {
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
		if text == "" {
			return m, nil
		}
		if cmd, args := parseSlashCommand(text); cmd != "" {
			m.session.resetInput()
			next := m.runHubSlashCommand(cmd, args)
			return m, next
		}
		composerMode := m.sessionComposerMode()
		if composerMode == hubComposerModeSteer {
			if strings.TrimSpace(m.detail.ActiveTurnID) == "" {
				m.addSessionSystem("Steer is not available until an active turn starts.")
				return m, nil
			}
			ref, ok := m.currentRef()
			if !ok {
				m.addSessionSystem("Session ref is invalid.")
				return m, nil
			}
			return m, sendHubSteer(m.client, ref, m.detail.ActiveTurnID, text)
		}
		if composerMode == hubComposerModeReadOnly || !m.detail.Capabilities.Send {
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
		m.session.addHistory(text)
		m.session.resetInput()
		m.session.refreshViewport()
		return m, sendHubInput(m.client, ref, text, draft)
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
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.browseSelected = -1
	m.authLoginProvider = ""
	m.authLoginFlowID = ""
	m.mode = hubModeDashboard
	m.clampSelection()
}

func (m *hubModel) openSpawnForm() {
	returnMode := m.mode
	dir := m.spawnWorkingDir()
	project := ""
	if m.mode == hubModeProject {
		if p, ok := m.selectedProject(); ok {
			project = p.Name
		}
	} else if len(m.rows) > 0 && m.selected >= 0 && m.selected < len(m.rows) {
		project = m.rows[m.selected].project
	}
	m.resetSpawnForm()
	m.spawnReturnMode = returnMode
	m.spawnDir = dir
	m.spawnProject = project
	m.mode = hubModeSpawn
	m.err = nil
	m.setSpawnFocus(hubSpawnFieldPrompt)
}

func (m *hubModel) closeSpawnForm() {
	returnMode := m.spawnReturnMode
	if returnMode != hubModeProject {
		returnMode = hubModeDashboard
	}
	m.resetSpawnForm()
	m.mode = returnMode
	m.clampSelection()
}

func (m *hubModel) resetSpawnForm() {
	m.spawnReturnMode = hubModeDashboard
	m.spawnDir = ""
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
	m.session.resetInput()
	if envModel := strings.TrimSpace(os.Getenv("SERF_MODEL")); strings.Contains(envModel, "/") {
		m.spawnModel = envModel
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

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func (m hubModel) spawnWorkingDir() string {
	if m.mode == hubModeProject {
		if p, ok := m.selectedProject(); ok {
			return p.WorkingDir
		}
		return ""
	}
	if len(m.rows) == 0 || m.selected < 0 || m.selected >= len(m.rows) {
		return ""
	}
	return m.workingDirForProjectKey(m.rows[m.selected].projectKey)
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

func (m *hubModel) applyHubNotification(notification appwire.Notification) tea.Cmd {
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
			m.detail.State = params.Status.Type
			m.session.processing = params.Status.Type == appwire.ThreadStatusProcessing
			if params.Status.Type != appwire.ThreadStatusProcessing && m.client != nil {
				if ref, ok := m.currentRef(); ok {
					cmd = fetchHubSession(m.client, ref)
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
			m.applyAgentMessageDelta(params.ItemID, params.Delta)
		}
	case appwire.NotifyToolOutputDelta:
		var params struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			reducer := m.sessionTranscriptReducer()
			reducer.applyToolOutputDelta(params.ItemID, params.Delta)
			m.applySessionTranscriptReducer(reducer)
		}
	case appwire.NotifyTurnCompleted:
		var params struct {
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			if params.Turn.ID != "" && params.Turn.ID == m.detail.ActiveTurnID {
				m.detail.ActiveTurnID = ""
			}
			if params.Turn.Status == appwire.TurnStatusFailed {
				m.addSessionSystemOnce(formatHubTurnError(params.Turn.Error, "Session error"))
			}
		}
	case appwire.NotifyWarning:
		var params struct {
			Message string `json:"message"`
			Source  string `json:"source"`
			Title   string `json:"title"`
			Warning struct {
				Message string `json:"message"`
			} `json:"warning"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			message := params.Message
			if strings.TrimSpace(message) == "" {
				message = params.Warning.Message
			}
			m.addSessionSystemOnce(formatHubDiagnostic(params.Title, params.Source, message, "Session warning"))
		}
	}
	m.session.refreshViewport()
	return cmd
}

func (m *hubModel) setActiveTurnID(turnID string) {
	m.detail.ActiveTurnID = turnID
}

func (m hubModel) notificationMatchesCurrentSession(notification appwire.Notification) bool {
	var params struct {
		Ref      string `json:"ref"`
		ThreadID string `json:"threadId"`
	}
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

func (m *hubModel) applyAgentMessageDelta(itemID, delta string) {
	reducer := m.sessionTranscriptReducer()
	reducer.applyAgentMessageDelta(itemID, delta)
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
		if !n.Live {
			return
		}
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
		var live []hubTreeNode
		for _, n := range p.Sessions {
			if n.Live {
				live = append(live, n)
			}
		}
		if len(live) == 0 {
			continue
		}
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		ensureGroup(key, p.Name, p.RollupState)
		for _, n := range live {
			addSession(key, p.Name, n)
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
		rows = append(rows, hubRow{
			kind:       hubRowProject,
			title:      group.name,
			project:    group.name,
			projectKey: group.key,
			state:      group.state,
			live:       true,
			rowID:      "project:" + group.key,
			updatedAt:  group.updatedAt,
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

func (m *hubModel) openProject(key string) {
	if key == "" {
		return
	}
	m.mode = hubModeProject
	m.selectedProjectKey = key
	m.projectRows = m.rowsForSelectedProject()
	m.selected = 0
	m.clampSelection()
}

func (m hubModel) selectedProject() (hubTreeProject, bool) {
	for _, p := range m.tree.Projects {
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		if key == m.selectedProjectKey {
			return p, true
		}
	}
	return hubTreeProject{}, false
}

func (m hubModel) rowsForSelectedProject() []hubRow {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	return buildProjectRows(project)
}

func (m hubModel) firstProjectHistoryKey() string {
	for _, project := range m.tree.Projects {
		if len(project.Sessions) == 0 && project.WorkingDir == "" {
			continue
		}
		key := project.Key
		if key == "" {
			key = hubProjectKey(project.Name)
		}
		return key
	}
	return ""
}

func (m hubModel) dashboardRows() []hubRow {
	return filterHubRows(m.rows, m.dashboardFilter.Value())
}

func (m hubModel) projectDisplayRows() []hubRow {
	return filterHubRows(m.projectRows, m.dashboardFilter.Value())
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
	if m.mode == hubModeProject {
		n = len(m.projectDisplayRows())
	}
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
	if m.mode == hubModeProject {
		return m.projectView()
	}
	return m.dashboardView()
}

func (m hubModel) dashboardView() string {
	rows := m.dashboardRows()
	liveCount := 0
	for _, row := range rows {
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
			Footer:  actionBarForWidth(m.width, "↑/↓ select", "enter open", "p project", "n new", "/ palette", "ctrl+o dashboard", "q quit"),
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
			}.View()
		}
		b.WriteString("No live sessions are running.\n\n")
		return appShell{
			TopBar: topBar,
			Body:   b.String(),
			Footer: emptyDashboardFooter(m.firstProjectHistoryKey() != "", width),
		}.View()
	}
	if m.dashboardUsesWideLayout() {
		drawerWidth := min(72, max(42, width/2))
		listWidth := max(40, width-drawerWidth-2)
		list := renderDashboardRows(rows, m.selected, listWidth, false)
		drawer := m.dashboardDetailsView(rows, drawerWidth)
		b.WriteString(joinDashboardColumns(list, drawer, listWidth, drawerWidth, width))
	} else {
		b.WriteString(renderDashboardRows(rows, m.selected, width, width <= 72))
	}
	b.WriteString("\n")
	return appShell{
		TopBar: topBar,
		Body:   b.String(),
		Footer: dashboardFooter(width),
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
	var b strings.Builder
	for i, row := range rows {
		if row.kind == hubRowProject {
			b.WriteString(renderDashboardProjectRow(row, rows, i == selected, width))
			b.WriteString("\n")
			continue
		}
		b.WriteString(renderDashboardSessionRow(row, i == selected, width, compact))
		b.WriteString("\n")
	}
	return b.String()
}

func renderDashboardProjectRow(row hubRow, rows []hubRow, selected bool, width int) string {
	cursor := " "
	if selected {
		cursor = ">"
	}
	line := fmt.Sprintf("%s %s %s  %s", cursor, statusDot(row.state), row.project, projectSummary(row, rows))
	return truncateText(line, width)
}

func renderDashboardSessionRow(row hubRow, selected bool, width int, compact bool) string {
	cursor := " "
	if selected {
		cursor = ">"
	}
	if compact {
		line := strings.Join(nonEmptyStrings([]string{
			cursor,
			"  " + statusDot(row.state),
			stateLabel(row.state),
			row.title,
			row.model,
			row.age,
		}), " ")
		return truncateText(line, width)
	}
	line := strings.Join(nonEmptyStrings([]string{
		cursor,
		"  " + statusDot(row.state),
		stateLabel(row.state),
		row.sourceLabel,
		row.title,
		row.model,
		row.age,
	}), " ")
	return truncateText(line, width)
}

func dashboardFooter(width int) string {
	if width <= 72 {
		return truncateText("keys: up/down enter p n new / palette ctrl+o dashboard q", width)
	}
	return truncateText("↑/↓ select  enter open  p project  n new  / palette  ctrl+o dashboard  q quit", width)
}

func emptyDashboardFooter(hasProjectHistory bool, width int) string {
	items := []string{"n new session"}
	if hasProjectHistory {
		items = append(items, "p project history")
	}
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
		padding := leftWidth - len([]rune(leftLine))
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
		return truncateMultilineText(strings.Join([]string{
			"details",
			"Diagnostic",
			"Message:  " + m.err.Error(),
			"Next:     refresh dashboard or check Hub health",
		}, "\n"), width)
	}
	if len(rows) == 0 || m.selected >= len(rows) {
		return truncateMultilineText("details\nNo dashboard row selected.", width)
	}
	row := rows[m.selected]
	switch row.kind {
	case hubRowProject:
		return truncateMultilineText(m.dashboardProjectDetails(row, rows), width)
	case hubRowSession:
		return truncateMultilineText(dashboardSessionDetails(row), width)
	default:
		return truncateMultilineText("details\nNo dashboard row selected.", width)
	}
}

func (m hubModel) dashboardProjectDetails(row hubRow, rows []hubRow) string {
	var b strings.Builder
	b.WriteString("details\n")
	fmt.Fprintf(&b, "Project:  %s\n", row.project)
	fmt.Fprintf(&b, "Live:     %d\n", projectLiveCount(row, rows))
	fmt.Fprintf(&b, "State:    %s\n", stateLabel(row.state))
	if dir := m.workingDirForProjectKey(row.projectKey); dir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", dir)
	}
	b.WriteString("Action:   enter opens project")
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
	count := 0
	for _, row := range rows {
		if row.kind == hubRowSession && row.projectKey == project.projectKey {
			count++
		}
	}
	return count
}

func truncateMultilineText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = truncateText(line, width)
	}
	return strings.Join(lines, "\n")
}

func (m hubModel) projectView() string {
	var b strings.Builder
	rows := m.projectDisplayRows()
	project, ok := m.selectedProject()
	name := m.selectedProjectKey
	if ok {
		name = project.Name
	}
	liveCount := 0
	recentCount := 0
	for _, row := range rows {
		if row.live {
			liveCount++
		} else {
			recentCount++
		}
	}
	topBar := fmt.Sprintf("serf / project / %s                         %d live · %d recent", name, liveCount, recentCount)
	if m.err != nil {
		fmt.Fprintf(&b, "error: %v\n\n", m.err)
	}
	if m.commandPalette != nil {
		return appShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.commandPalette.View(),
			Footer:  actionBarForWidth(m.width, "↑/↓ select", "enter open", "r resume", "n new here", "/ palette", "esc dashboard", "ctrl+o dashboard"),
		}.View()
	}
	if m.dashboardFilterActive || strings.TrimSpace(m.dashboardFilter.Value()) != "" {
		b.WriteString(m.dashboardFilter.View())
		b.WriteString("\n\n")
	}
	idx := 0
	if liveCount > 0 {
		b.WriteString("Live now\n")
		for _, row := range rows {
			if !row.live {
				continue
			}
			writeProjectRow(&b, row, idx == m.selected)
			idx++
		}
		b.WriteString("\n")
	}
	if recentCount > 0 {
		b.WriteString("Recent in this project\n")
		for _, row := range rows {
			if row.live {
				continue
			}
			writeProjectRow(&b, row, idx == m.selected)
			idx++
		}
	} else if strings.TrimSpace(m.dashboardFilter.Value()) != "" {
		b.WriteString("No sessions match this filter.\n")
	} else {
		b.WriteString("No ended sessions in this project yet.\n")
	}
	return appShell{
		TopBar: topBar,
		Body:   b.String(),
		Footer: actionBarForWidth(m.width, "↑/↓ select", "enter open", "r resume", "n new here", "/ palette", "esc dashboard", "ctrl+o dashboard"),
	}.View()
}

func writeProjectRow(b *strings.Builder, row hubRow, selected bool) {
	cursor := " "
	if selected {
		cursor = ">"
	}
	fmt.Fprintf(b, "%s %s %-10s %-12s %-34s %-10s %s\n", cursor, statusDot(row.state), stateLabel(row.state), row.sourceLabel, row.title, row.model, row.age)
}

func statusDot(state string) string {
	switch stateLabel(state) {
	case "awaiting":
		return "●"
	case "processing":
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
	case "awaiting", "awaiting_reply", "needs-input":
		return "awaiting"
	case "processing", "running", "working":
		return "processing"
	case "warning", "warn":
		return "warning"
	case "idle", "ready":
		return "idle"
	case "ended", "done", "closed":
		return "ended"
	default:
		if strings.TrimSpace(state) == "" {
			return "ended"
		}
		return state
	}
}

func projectSummary(project hubRow, rows []hubRow) string {
	count := 0
	attention := stateLabel(project.state)
	for _, row := range rows {
		if row.kind != hubRowSession || row.projectKey != project.projectKey {
			continue
		}
		count++
		if attentionRankLabel(row.state) > attentionRankLabel(attention) {
			attention = stateLabel(row.state)
		}
	}
	return fmt.Sprintf("%d live · %s", count, attention)
}

func attentionRankLabel(state string) int {
	switch stateLabel(state) {
	case "awaiting":
		return 4
	case "processing":
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
	if m.spawnDir != "" {
		fmt.Fprintf(&b, "  Dir:      %s\n", m.spawnDir)
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
	fmt.Fprintf(&footer, "%s Prompt (optional):\n", m.spawnFieldPrefix(hubSpawnFieldPrompt))
	for _, line := range strings.Split(strings.TrimSuffix(renderComposerDraft(m.session.input.Value()), "\n"), "\n") {
		footer.WriteString("  ")
		footer.WriteString(line)
		footer.WriteString("\n")
	}
	keys := []string{"tab: next field", "shift+tab: previous", m.spawnFieldHint(), "esc: cancel", "ctrl+o: dashboard"}
	footer.WriteString("\n")
	footer.WriteString(actionBarForWidth(m.width, keys...))
	return appShell{
		TopBar:  topBar,
		Body:    b.String(),
		Overlay: overlay,
		Footer:  footer.String(),
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
	case hubComposerModeSteer:
		return "steer: ready"
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
	if m.commandPalette != nil {
		overlay.WriteString(m.commandPalette.View())
		overlay.WriteString("\n\n")
	}
	var footer string
	switch {
	case m.transcriptView != nil:
		footer = actionBarForWidth(m.width, "esc/i/q: return to chat", "ctrl+o: dashboard")
	case m.session.scrollMode:
		keys := []string{"esc/i/q: compose"}
		if m.detail.Capabilities.Fork {
			keys = append(keys, "f: fork")
		}
		keys = append(keys, "ctrl+o: dashboard")
		footer = actionBarForWidth(m.width, keys...)
	default:
		footer = m.sessionComposerPanel().View()
	}
	overlayText := overlay.String()
	bodyHeight := sessionShellBodyHeight(m.height, topBar, overlayText, footer)
	body := m.sessionBody(mainBody, bodyHeight)
	return appShell{
		TopBar:  topBar,
		Body:    body,
		Overlay: overlayText,
		Footer:  footer,
	}.View()
}

func (m hubModel) renderSessionDetails() string {
	return detailsDrawer{Detail: m.detail, HubURL: m.hubURL}.View()
}

func (m hubModel) sessionBody(mainBody string, bodyHeight int) string {
	if m.sessionPanel == nil {
		return mainBody
	}

	panel := m.sessionPanel.View()
	if bodyHeight <= 0 {
		if m.width >= 120 {
			drawerWidth := min(72, max(42, m.width/3))
			bodyWidth := max(40, m.width-drawerWidth-2)
			return joinDashboardColumns(mainBody, panel, bodyWidth, drawerWidth, m.width)
		}
		return panel + "\n\n" + mainBody
	}
	if m.width >= 120 {
		drawerWidth := min(72, max(42, m.width/3))
		bodyWidth := max(40, m.width-drawerWidth-2)
		return joinDashboardColumns(
			limitSessionBodyLines(mainBody, bodyHeight),
			limitFirstLines(panel, bodyHeight),
			bodyWidth,
			drawerWidth,
			m.width,
		)
	}

	panelLines := multilineLines(panel)
	if len(panelLines) >= bodyHeight {
		return strings.Join(panelLines[:bodyHeight], "\n")
	}
	remaining := bodyHeight - len(panelLines)
	if remaining > 0 {
		remaining--
	}
	main := limitSessionBodyLines(mainBody, remaining)
	if strings.TrimSpace(main) == "" {
		return strings.Join(panelLines, "\n")
	}
	return strings.Join(panelLines, "\n") + "\n\n" + main
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
