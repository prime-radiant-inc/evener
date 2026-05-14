package main

import (
	"encoding/json"
	"fmt"
	"os"
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
}

type hubForkDraft struct {
	Ref          appwire.Ref
	Turn         int
	OriginalText string
	Label        string
}

type hubModel struct {
	client *appwire.Client
	hubURL string
	width  int
	height int
	err    error

	mode     hubMode
	tree     hubTreeResponse
	rows     []hubRow
	selected int

	dashboardFilter       textinput.Model
	dashboardFilterActive bool
	commandPalette        *commandPalette

	selectedProjectKey string
	projectRows        []hubRow
	browseSelected     int
	forkDraft          *hubForkDraft
	sessionThemePicker *themePicker
	sessionModelPicker *modelPicker
	spawnReturnMode    hubMode
	spawnDir           string
	spawnProject       string
	spawnHarness       string
	spawnHarnesses     []string
	spawnHarnessKinds  map[string]string
	spawnModel         string
	spawnModels        []modelPickerItem
	spawnHarnessModels map[string][]modelPickerItem
	spawnModelPicker   *modelPicker
	spawnSubmitting    bool
	spawnFocus         hubSpawnField

	detail  hubSessionDetail
	session model
}

func newHubModel(client *appwire.Client, hubURL string) hubModel {
	session := newModel("", "", nil)
	return hubModel{client: client, hubURL: hubURL, session: session, browseSelected: -1, dashboardFilter: newHubFilterInput()}
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
			m.session.messages = append(m.session.messages, chatMessage{Kind: msgSystem, Text: m.renderSessionDetails()})
			m.session.refreshViewport()
			return m, nil
		}
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
		return m, nil
	case hubNotificationMsg:
		if !msg.ok {
			return m, nil
		}
		m.applyHubNotification(msg.notification)
		return m, waitHubNotification(m.client)
	case hubSendMsg:
		if msg.err != nil {
			m.session.setInputValue(msg.text)
			m.addSessionSystem("Send failed: " + msg.err.Error())
		} else {
			m.setActiveTurnID(msg.turnID)
		}
		return m, nil
	case hubTasksMsg:
		if msg.err != nil {
			m.addSessionSystem("Tasks error: " + msg.err.Error())
		} else {
			m.addSessionSystem(renderTasks(msg.tasks, m.width))
		}
		return m, nil
	case hubActionMsg:
		if msg.err != nil {
			m.addSessionSystem(fmt.Sprintf("%s failed: %s", msg.action, msg.err))
			return m, nil
		}
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
			m.addSessionSystem("Clear failed: " + msg.err.Error())
			return m, nil
		}
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Clear returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubForkMsg:
		if msg.err != nil {
			m.addSessionSystem("Fork failed: " + msg.err.Error())
			return m, nil
		}
		m.forkDraft = nil
		ref, err := appwire.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Fork returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case hubSpawnMsg:
		m.spawnSubmitting = false
		if msg.err != nil {
			m.err = fmt.Errorf("spawn failed: %w", msg.err)
			return m, nil
		}
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
			m.addSessionSystem(fmt.Sprintf("Could not fetch models: %s\nUse /model <name> to switch manually.", msg.err))
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
	}
	return m, nil
}

func (m hubModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.commandPalette != nil {
		return m.updateCommandPaletteKey(msg)
	}
	switch msg.String() {
	case "ctrl+o":
		m.returnToDashboard()
		return m, nil
	case "ctrl+c":
		if m.mode == hubModeSession {
			return m.updateSessionKey(msg)
		}
		return m, tea.Quit
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
	}
	palette := newCommandPalette("Command palette", commandPaletteEntriesForRows(m.mode, rows), width)
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
	switch command {
	case "new":
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client, m.spawnDir)
		}
		return m, nil
	case "refresh":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
		return m, nil
	default:
		return m, nil
	}
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
	if m.spawnHarnessUsesSerfModels() && strings.TrimSpace(m.spawnModel) == "" {
		m.err = fmt.Errorf("choose a model before spawning")
		return m, nil
	}
	req := hubSpawnRequest{
		Prompt:     strings.TrimSpace(m.session.input.Value()),
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
				setTheme(picker.selected)
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

	if m.forkDraft != nil {
		switch msg.String() {
		case "esc":
			m.forkDraft = nil
			m.session.resetInput()
			m.enterSessionBrowse(false)
			m.addSessionSystem("Fork cancelled.")
			return m, nil
		case "enter":
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
			m.session.resetInput()
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

	if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
		prevHeight := m.session.input.Height()
		m.session.input.InsertString("\n")
		m.resizeSessionInputFrom(prevHeight)
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.enterSessionBrowse(false)
		return m, nil
	case "up":
		if m.session.input.Line() == 0 && len(m.session.history) > 0 {
			if m.session.historyIdx == -1 {
				m.session.historyDraft = m.session.input.Value()
				m.session.historyIdx = len(m.session.history) - 1
			} else if m.session.historyIdx > 0 {
				m.session.historyIdx--
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
	case "ctrl+c":
		if !m.detail.Capabilities.Interrupt {
			m.addSessionSystem("Interrupt is not available for this session.")
			return m, nil
		}
		if strings.TrimSpace(m.detail.ActiveTurnID) == "" {
			m.addSessionSystem("Interrupt is not available until an active turn starts.")
			return m, nil
		}
		ref, ok := m.currentRef()
		if !ok {
			m.addSessionSystem("Session ref is invalid.")
			return m, nil
		}
		return m, sendHubAction(m.client, ref, "interrupt", m.detail.ActiveTurnID)
	}

	if msg.String() == "enter" {
		text := strings.TrimSpace(m.session.input.Value())
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
		m.session.messages = append(m.session.messages, chatMessage{Kind: msgUser, Text: text})
		m.session.lastSentText = text
		m.session.addHistory(text)
		m.session.resetInput()
		m.session.refreshViewport()
		return m, sendHubInput(m.client, ref, text)
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSessionInputFrom(prevHeight)
	return m, cmd
}

func (m *hubModel) runHubSlashCommand(cmd, args string) tea.Cmd {
	ref, ok := m.currentRef()
	if !ok {
		m.addSessionSystem("Session ref is invalid.")
		return nil
	}
	switch cmd {
	case "dashboard":
		m.returnToDashboard()
		return nil
	case "project":
		key, ok := m.projectKeyForSession()
		if !ok {
			m.addSessionSystem("Project is not available for this session.")
			return nil
		}
		m.openProject(key)
		return nil
	case "projects":
		m.addSessionSystem("Project switcher is not available yet. Use /project for this session or ctrl+o for the live dashboard.")
		return nil
	case "tasks":
		return fetchHubTasks(m.client, ref)
	case "details", "status":
		return fetchHubSession(m.client, ref)
	case "interrupt":
		if !m.detail.Capabilities.Interrupt {
			m.addActionUnavailableNotice("interrupt", "Interrupt is not available for this session.", "")
			return nil
		}
		if strings.TrimSpace(m.detail.ActiveTurnID) == "" {
			m.addSessionSystem("Interrupt is not available until an active turn starts.")
			return nil
		}
		return sendHubAction(m.client, ref, "interrupt", m.detail.ActiveTurnID)
	case "compact":
		if !m.detail.Capabilities.Compact {
			m.addActionUnavailableNotice("compact", "Compact is not available for this session.", "")
			return nil
		}
		return sendHubAction(m.client, ref, "compact", "")
	case "clear":
		if !m.detail.Capabilities.Clear {
			m.addActionUnavailableNotice("clear", "Clear is not available for this session.", "")
			return nil
		}
		return sendHubClear(m.client, ref)
	case "shutdown":
		if !m.detail.Capabilities.Shutdown {
			m.addActionUnavailableNotice("shutdown", "Shutdown is not available for this session.", "")
			return nil
		}
		return sendHubAction(m.client, ref, "shutdown", "")
	case "model":
		model := strings.TrimSpace(args)
		if !m.detail.Capabilities.ChangeModel {
			m.addActionUnavailableNotice("change model", "Model change is not available for this session.", "source does not advertise change model")
			return nil
		}
		if model == "" {
			if m.client == nil {
				m.addSessionSystem("Model picker is not available without a hub client.")
				return nil
			}
			m.addSessionSystem("Fetching available models...")
			return fetchHubSessionModels(m.client, m.detail.WorkingDir)
		}
		return sendHubAction(m.client, ref, model, "")
	case "theme":
		picker := newThemePicker()
		m.sessionThemePicker = &picker
		return nil
	case "help":
		m.addSessionSystem(hubSlashCommandHelp(m.detail.Capabilities))
		return nil
	default:
		m.addSessionSystem("Unknown command: /" + cmd)
		return nil
	}
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
		if len(models) > 0 {
			m.spawnModel = models[0].id
		}
	}
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

func (m *hubModel) applyHubNotification(notification appwire.Notification) {
	if m.mode != hubModeSession {
		return
	}
	if !m.notificationMatchesCurrentSession(notification) {
		return
	}
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
			if idx, ok := m.session.activeTools[params.ItemID]; ok && idx < len(m.session.messages) && m.session.messages[idx].Tool != nil {
				m.session.messages[idx].Tool.Output += params.Delta
			}
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
	var rows []hubRow
	seen := map[string]bool{}
	projectIndexes := map[string]int{}

	addProject := func(key, name, state string) {
		if name == "" {
			name = "(no project)"
		}
		if key == "" {
			key = hubProjectKey(name)
		}
		if _, ok := projectIndexes[key]; ok {
			return
		}
		projectIndexes[key] = len(rows)
		rows = append(rows, hubRow{
			kind:       hubRowProject,
			title:      name,
			project:    name,
			projectKey: key,
			state:      state,
			live:       true,
			rowID:      "project:" + key,
		})
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
		rows = append(rows, hubRow{
			kind:        hubRowSession,
			ref:         ref,
			sourceLabel: sourceLabelFromRef(ref),
			title:       title,
			project:     project,
			projectKey:  key,
			state:       n.State,
			live:        n.Live,
			model:       n.Model,
			age:         n.Age,
			rowID:       rowID,
		})
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
		addProject(key, p.Name, p.RollupState)
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
		addProject(key, project, n.State)
		addSession(key, project, n)
	}

	return rows
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
		row := hubRow{
			kind:        hubRowSession,
			ref:         ref,
			sourceLabel: sourceLabelFromRef(ref),
			title:       title,
			project:     project.Name,
			projectKey:  key,
			state:       state,
			live:        n.Live,
			model:       n.Model,
			age:         n.Age,
			rowID:       rowID,
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

func (m hubModel) dashboardRows() []hubRow {
	return filterHubRows(m.rows, m.dashboardFilter.Value())
}

func (m hubModel) projectDisplayRows() []hubRow {
	return filterHubRows(m.projectRows, m.dashboardFilter.Value())
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
	var b strings.Builder
	rows := m.dashboardRows()
	liveCount := 0
	for _, row := range rows {
		if row.kind == hubRowSession && row.live {
			liveCount++
		}
	}
	fmt.Fprintf(&b, "serf live                                      %s · %d live\n\n", m.hubURL, liveCount)
	if m.err != nil {
		fmt.Fprintf(&b, "error: %v\n\n", m.err)
	}
	if m.commandPalette != nil {
		b.WriteString(m.commandPalette.View())
		b.WriteString("\n")
		return b.String()
	}
	if m.dashboardFilterActive || strings.TrimSpace(m.dashboardFilter.Value()) != "" {
		b.WriteString(m.dashboardFilter.View())
		b.WriteString("\n\n")
	}
	if len(rows) == 0 {
		if strings.TrimSpace(m.dashboardFilter.Value()) != "" {
			b.WriteString("No sessions match this filter.\n\n")
			b.WriteString("esc clear filter\n")
			return b.String()
		}
		b.WriteString("No live sessions are running.\n\n")
		b.WriteString("n new session\n")
		b.WriteString("/ palette\n")
		b.WriteString("q quit\n")
		return b.String()
	}
	for i, row := range rows {
		cursor := " "
		if i == m.selected {
			cursor = ">"
		}
		if row.kind == hubRowProject {
			fmt.Fprintf(&b, "%s %s %-28s %s\n", cursor, statusDot(row.state), row.project, projectSummary(row, rows))
			continue
		}
		fmt.Fprintf(&b, "%s   %s %-10s %-12s %-28s %-10s %s\n", cursor, statusDot(row.state), stateLabel(row.state), row.sourceLabel, row.title, row.model, row.age)
	}
	b.WriteString("\n↑/↓ select  enter open  p project  n new  / palette  ctrl+o dashboard  q quit\n")
	return b.String()
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
	fmt.Fprintf(&b, "serf / project / %s                         %d live · %d recent\n\n", name, liveCount, recentCount)
	if m.err != nil {
		fmt.Fprintf(&b, "error: %v\n\n", m.err)
	}
	if m.commandPalette != nil {
		b.WriteString(m.commandPalette.View())
		b.WriteString("\n")
		return b.String()
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
	b.WriteString("\n↑/↓ select  enter open  r resume  n new here  / palette  esc dashboard\n")
	return b.String()
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
	b.WriteString("serf / new session\n\n")
	if m.spawnModelPicker != nil {
		b.WriteString(m.spawnModelPicker.View())
		b.WriteString("\n\n")
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
	if m.spawnSubmitting {
		b.WriteString("\nStarting session...\n")
	}
	fmt.Fprintf(&b, "\n%s Prompt (optional):\n", m.spawnFieldPrefix(hubSpawnFieldPrompt))
	for _, line := range strings.Split(strings.TrimSuffix(renderComposerDraft(m.session.input.Value()), "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	keys := []string{"tab: next field", "shift+tab: previous", m.spawnFieldHint(), "esc: cancel", "ctrl+o: dashboard"}
	b.WriteString("\n\n")
	b.WriteString(strings.Join(keys, "  "))
	b.WriteString("\n")
	return b.String()
}

func (m hubModel) sessionView() string {
	var b strings.Builder
	title := m.detail.Title
	if title == "" {
		title = m.detail.SessionID
	}
	fmt.Fprintf(&b, "%s\n", title)
	fmt.Fprintf(&b, "%s  %s  %s  %s\n", m.detail.SourceLabel, m.detail.Ref, m.detail.State, m.detail.WorkingDir)
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if len(m.session.messages) == 0 {
		b.WriteString("\nNo transcript events yet.\n")
	} else {
		width := m.width
		if width == 0 {
			width = 100
		}
		for i, msg := range m.session.messages {
			rendered := renderMessage(msg, width, m.session.isToolFocused(i))
			if rendered == "" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(rendered)
			b.WriteString("\n")
		}
	}
	if m.sessionModelPicker != nil {
		b.WriteString("\n")
		b.WriteString(m.sessionModelPicker.View())
		b.WriteString("\n")
	}
	if m.sessionThemePicker != nil {
		b.WriteString("\n")
		b.WriteString(m.sessionThemePicker.View())
		b.WriteString("\n")
	}
	switch {
	case m.session.scrollMode:
		keys := []string{"esc/i/q: compose"}
		if m.detail.Capabilities.Fork {
			keys = append(keys, "f: fork")
		}
		keys = append(keys, "ctrl+o: dashboard")
		b.WriteString("\n")
		b.WriteString(strings.Join(keys, "  "))
		b.WriteString("\n")
	default:
		b.WriteString("\n")
		b.WriteString(m.sessionComposerPanel().View())
	}
	return b.String()
}

func (m hubModel) renderSessionDetails() string {
	return detailsDrawer{Detail: m.detail}.View()
}
