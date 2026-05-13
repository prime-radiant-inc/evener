package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

	selectedProjectKey string
	projectRows        []hubRow
	browseSelected     int
	forkDraft          *hubForkDraft
	spawnReturnMode    hubMode
	spawnDir           string
	spawnProject       string
	spawnHarness       string
	spawnHarnesses     []string
	spawnHarnessKinds  map[string]string
	spawnModel         string
	spawnModels        []modelPickerItem
	spawnModelPicker   *modelPicker
	spawnSubmitting    bool

	detail  hubSessionDetail
	session model
}

func newHubModel(client *appwire.Client, hubURL string) hubModel {
	session := newModel("", "", nil)
	return hubModel{client: client, hubURL: hubURL, session: session, browseSelected: -1}
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
		case "model":
			m.addSessionSystem("Model updated.")
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
				m.err = fmt.Errorf("models failed: %w", msg.err)
			}
			return m, nil
		}
		m.spawnModels = msg.models
		if m.mode == hubModeSpawn {
			m.syncSpawnModelWithHarness()
		}
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
		}
		return m, nil
	}
	return m, nil
}

func (m hubModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+o":
		m.returnToDashboard()
		return m, nil
	case "ctrl+c":
		if m.mode == hubModeSession {
			return m.updateSessionKey(msg)
		}
		return m, tea.Quit
	case "q":
		if m.mode == hubModeSpawn {
			return m.updateSpawnKey(msg)
		}
		if m.mode == hubModeSession && m.session.scrollMode {
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
	switch msg.String() {
	case "r":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
	case "s":
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client)
		}
		return m, nil
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.rows)-1 {
			m.selected++
		}
	case "p":
		if len(m.rows) > 0 {
			m.openProject(m.rows[m.selected].projectKey)
		}
	case "enter":
		if len(m.rows) > 0 {
			row := m.rows[m.selected]
			if row.kind == hubRowProject {
				m.openProject(row.projectKey)
				return m, nil
			}
			if m.client == nil {
				return m, nil
			}
			return m, fetchHubSession(m.client, row.ref)
		}
	}
	return m, nil
}

func (m hubModel) updateProjectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		m.mode = hubModeDashboard
		m.clampSelection()
	case "r":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
	case "s":
		m.openSpawnForm()
		if m.client != nil {
			return m, fetchHubSpawnOptions(m.client)
		}
		return m, nil
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.projectRows)-1 {
			m.selected++
		}
	case "enter":
		if len(m.projectRows) > 0 && m.client != nil {
			return m, fetchHubSession(m.client, m.projectRows[m.selected].ref)
		}
	}
	return m, nil
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
	case "m":
		models := m.spawnSelectableModels()
		if !m.spawnHarnessUsesHubModels() {
			m.err = fmt.Errorf("model selection is managed by the %s harness", m.spawnHarness)
			return m, nil
		}
		if len(models) == 0 {
			m.err = fmt.Errorf("no models available")
			return m, nil
		}
		picker := newModelPicker(models, m.spawnModel, m.width)
		picker.title = "Select spawn model"
		m.spawnModelPicker = &picker
		return m, nil
	case "h":
		m.cycleSpawnHarness()
		return m, nil
	case "enter":
		if m.client == nil || m.spawnSubmitting {
			return m, nil
		}
		if m.spawnHarnessUsesHubModels() && strings.TrimSpace(m.spawnModel) == "" {
			m.err = fmt.Errorf("choose a model before spawning")
			return m, nil
		}
		req := hubSpawnRequest{
			Task:       strings.TrimSpace(m.session.input.Value()),
			Harness:    strings.TrimSpace(m.spawnHarness),
			Model:      strings.TrimSpace(m.spawnModel),
			WorkingDir: m.spawnDir,
		}
		m.err = nil
		m.spawnSubmitting = true
		return m, sendHubSpawn(m.client, req)
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
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
	return m, cmd
}

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

	switch msg.String() {
	case "esc":
		m.enterSessionBrowse(false)
		return m, nil
	case "pgup":
		m.enterSessionBrowse(true)
		return m, nil
	case "ctrl+c":
		if !m.detail.Capabilities.Interrupt {
			m.addSessionSystem("Interrupt is not available for this session.")
			return m, nil
		}
		ref, ok := m.currentRef()
		if !ok {
			m.addSessionSystem("Session ref is invalid.")
			return m, nil
		}
		return m, sendHubAction(m.client, ref, "interrupt")
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
		if !m.detail.Capabilities.Send {
			m.addSessionSystem("Send is not available for this session.")
			return m, nil
		}
		ref, ok := m.currentRef()
		if !ok {
			m.addSessionSystem("Session ref is invalid.")
			return m, nil
		}
		m.session.messages = append(m.session.messages, chatMessage{Kind: msgUser, Text: text})
		m.session.lastSentText = text
		m.session.resetInput()
		m.session.refreshViewport()
		return m, sendHubInput(m.client, ref, text)
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
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
			m.addSessionSystem("Interrupt is not available for this session.")
			return nil
		}
		return sendHubAction(m.client, ref, "interrupt")
	case "compact":
		if !m.detail.Capabilities.Compact {
			m.addSessionSystem("Compact is not available for this session.")
			return nil
		}
		return sendHubAction(m.client, ref, "compact")
	case "clear":
		if !m.detail.Capabilities.Clear {
			m.addSessionSystem("Clear is not available for this session.")
			return nil
		}
		return sendHubClear(m.client, ref)
	case "model":
		model := strings.TrimSpace(args)
		if model == "" {
			m.addSessionSystem("Usage: /model <name>")
			return nil
		}
		if !m.detail.Capabilities.ChangeModel {
			m.addSessionSystem("Model change is not available for this session.")
			return nil
		}
		return sendHubAction(m.client, ref, model)
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
	m.session.input.Focus()
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
	m.spawnModelPicker = nil
	m.spawnSubmitting = false
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

func (m hubModel) spawnHarnessUsesHubModels() bool {
	return m.spawnHarnessKind() != "codex"
}

func (m hubModel) spawnSelectableModels() []modelPickerItem {
	if !m.spawnHarnessUsesHubModels() {
		return nil
	}
	return m.spawnModels
}

func (m *hubModel) syncSpawnModelWithHarness() {
	if !m.spawnHarnessUsesHubModels() {
		m.spawnModel = ""
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
			if len(m.session.messages) > 0 && m.session.messages[len(m.session.messages)-1].Kind == msgAssistant {
				m.session.messages[len(m.session.messages)-1].Text += params.Delta
			} else {
				m.session.messages = append(m.session.messages, chatMessage{Kind: msgAssistant, Text: params.Delta})
			}
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
	}
	m.session.refreshViewport()
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

func (m *hubModel) applyThreadItem(item appwire.ThreadItem, completed bool) {
	switch item.Type {
	case "user_message":
		if strings.TrimSpace(item.Text) != "" {
			m.session.messages = append(m.session.messages, chatMessage{Kind: msgUser, Text: item.Text, TurnIndex: turnIndexFromID(item.TurnID)})
		}
	case "agent_message":
		if strings.TrimSpace(item.Text) != "" {
			if len(m.session.messages) > 0 && m.session.messages[len(m.session.messages)-1].Kind == msgAssistant {
				m.session.messages[len(m.session.messages)-1].Text = item.Text
			} else {
				m.session.messages = append(m.session.messages, chatMessage{Kind: msgAssistant, Text: item.Text})
			}
		}
	case "tool_call":
		done := threadItemToolDone(item, completed)
		if idx, ok := m.activeHubToolIndex(item); ok {
			info := m.session.messages[idx].Tool
			if info == nil {
				return
			}
			mergeThreadItemIntoToolInfo(info, item, done)
			if done {
				m.clearActiveHubTool(item)
			} else {
				m.rememberActiveHubTool(item, idx)
			}
			return
		}
		info := toolInfoFromThreadItem(item, done)
		idx := len(m.session.messages)
		m.session.messages = append(m.session.messages, chatMessage{Kind: msgTool, Tool: info})
		if !done {
			m.rememberActiveHubTool(item, idx)
		}
	}
}

func (m *hubModel) activeHubToolIndex(item appwire.ThreadItem) (int, bool) {
	if item.ID != "" {
		if idx, ok := m.session.activeTools[item.ID]; ok && idx < len(m.session.messages) {
			return idx, true
		}
	}
	if item.CallID != "" {
		if idx, ok := m.session.activeTools[item.CallID]; ok && idx < len(m.session.messages) {
			return idx, true
		}
	}
	return 0, false
}

func (m *hubModel) rememberActiveHubTool(item appwire.ThreadItem, idx int) {
	if item.ID != "" {
		m.session.activeTools[item.ID] = idx
	}
	if item.CallID != "" {
		m.session.activeTools[item.CallID] = idx
	}
}

func (m *hubModel) clearActiveHubTool(item appwire.ThreadItem) {
	if item.ID != "" {
		delete(m.session.activeTools, item.ID)
	}
	if item.CallID != "" {
		delete(m.session.activeTools, item.CallID)
	}
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

func (m *hubModel) clampSelection() {
	n := len(m.rows)
	if m.mode == hubModeProject {
		n = len(m.projectRows)
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
	liveCount := 0
	for _, row := range m.rows {
		if row.kind == hubRowSession && row.live {
			liveCount++
		}
	}
	fmt.Fprintf(&b, "serf live                                      %s · %d live\n\n", m.hubURL, liveCount)
	if m.err != nil {
		fmt.Fprintf(&b, "error: %v\n\n", m.err)
	}
	if len(m.rows) == 0 {
		b.WriteString("No live sessions are running.\n\n")
		b.WriteString("s start a session\n")
		b.WriteString("/projects browse project history\n")
		b.WriteString("/ search sessions\n")
		b.WriteString("q quit\n")
		return b.String()
	}
	for i, row := range m.rows {
		cursor := " "
		if i == m.selected {
			cursor = ">"
		}
		if row.kind == hubRowProject {
			fmt.Fprintf(&b, "%s %s %-28s %s\n", cursor, statusDot(row.state), row.project, projectSummary(row, m.rows))
			continue
		}
		fmt.Fprintf(&b, "%s   %s %-10s %-12s %-28s %-10s %s\n", cursor, statusDot(row.state), stateLabel(row.state), row.sourceLabel, row.title, row.model, row.age)
	}
	b.WriteString("\n↑/↓ select  enter open  p project  s spawn  / filter  ctrl+o dashboard  q quit\n")
	return b.String()
}

func (m hubModel) projectView() string {
	var b strings.Builder
	project, ok := m.selectedProject()
	name := m.selectedProjectKey
	if ok {
		name = project.Name
	}
	liveCount := 0
	recentCount := 0
	for _, row := range m.projectRows {
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
	idx := 0
	if liveCount > 0 {
		b.WriteString("Live now\n")
		for _, row := range m.projectRows {
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
		for _, row := range m.projectRows {
			if row.live {
				continue
			}
			writeProjectRow(&b, row, idx == m.selected)
			idx++
		}
	} else {
		b.WriteString("No ended sessions in this project yet.\n")
	}
	b.WriteString("\n↑/↓ select  enter open  r resume  s spawn here  / filter  esc dashboard\n")
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
		b.WriteString("\n")
		return b.String()
	}
	model := m.spawnModel
	models := m.spawnSelectableModels()
	if !m.spawnHarnessUsesHubModels() {
		model = "(harness default)"
	} else if model == "" && len(models) == 0 {
		model = "(loading models...)"
	} else if model == "" {
		model = "(choose a model)"
	}
	fmt.Fprintf(&b, "Harness:  %s\n", m.spawnHarness)
	fmt.Fprintf(&b, "Model:    %s\n", model)
	if m.spawnProject != "" {
		fmt.Fprintf(&b, "Project:  %s\n", m.spawnProject)
	}
	if m.spawnDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", m.spawnDir)
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if m.spawnSubmitting {
		b.WriteString("\nStarting session...\n")
	}
	b.WriteString("\nTask (optional):\n")
	b.WriteString("> ")
	b.WriteString(m.session.input.Value())
	keys := []string{"h: harness"}
	if m.spawnHarnessUsesHubModels() {
		keys = append(keys, "m: model")
	}
	keys = append(keys, "enter: spawn", "esc: cancel", "ctrl+o: dashboard")
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
	switch {
	case m.forkDraft != nil:
		b.WriteString("\nfork draft: enter fork  esc cancel  ctrl+o: dashboard\n")
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
		keys := []string{}
		if m.detail.Capabilities.Send {
			keys = append(keys, "enter: send")
		}
		keys = append(keys, "esc: browse", "ctrl+o: dashboard", "/help")
		b.WriteString("\n")
		b.WriteString(strings.Join(keys, "  "))
		b.WriteString("\n")
	}
	if v := strings.TrimSpace(m.session.input.Value()); v != "" {
		b.WriteString("> ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	return b.String()
}

func (m hubModel) renderSessionDetails() string {
	var b strings.Builder
	if m.detail.SessionID != "" {
		fmt.Fprintf(&b, "Session:  %s\n", m.detail.SessionID)
	}
	if m.detail.SourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", m.detail.SourceLabel)
	}
	if m.detail.Model != "" || m.detail.Profile != "" {
		fmt.Fprintf(&b, "Model:    %s (%s)\n", m.detail.Model, m.detail.Profile)
	}
	if m.detail.WorkingDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", m.detail.WorkingDir)
	}
	if m.detail.Branch != "" {
		fmt.Fprintf(&b, "Branch:   %s\n", m.detail.Branch)
	}
	if m.detail.TurnCount > 0 {
		fmt.Fprintf(&b, "Turns:    %d\n", m.detail.TurnCount)
	}
	return strings.TrimSpace(b.String())
}
