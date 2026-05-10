package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

type hubMode int

const (
	hubModeDashboard hubMode = iota
	hubModeProject
	hubModeSession
)

type hubRowKind int

const (
	hubRowProject hubRowKind = iota
	hubRowSession
)

type hubRow struct {
	kind       hubRowKind
	ref        hubapi.Ref
	title      string
	project    string
	projectKey string
	state      string
	live       bool
	model      string
	age        string
	rowID      string
}

type hubModel struct {
	client *hubapi.Client
	hubURL string
	width  int
	height int
	err    error

	mode     hubMode
	tree     hubapi.TreeResponse
	rows     []hubRow
	selected int

	selectedProjectKey string
	projectRows        []hubRow

	detail        hubapi.SessionDetail
	session       model
	streamCancel  context.CancelFunc
	streamDone    bool
	streamLastID  string
	streamStarted bool
}

func newHubModel(client *hubapi.Client, hubURL string) hubModel {
	session := newModel("", "", nil)
	return hubModel{client: client, hubURL: hubURL, session: session}
}

func (m hubModel) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return fetchHubTree(m.client)
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
		m.session.viewport.Width = m.width
		m.session.viewport.Height = m.session.vpHeight()
		m.session.refreshViewport()
		return m, m.startSessionStream()
	case hubStreamStartedMsg:
		if m.streamCancel != nil {
			m.streamCancel()
		}
		m.streamCancel = msg.cancel
		m.streamStarted = true
		m.streamDone = false
		m.streamLastID = ""
		return m, waitHubStream(msg.ch)
	case hubStreamMsg:
		cmd := waitHubStream(msg.ch)
		switch ev := msg.msg.(type) {
		case sseConnectedMsg:
			return m, cmd
		case sseEventMsg:
			m.applyHubSSEEvent(SSEEvent(ev))
			return m, cmd
		case sseErrorMsg:
			if !m.streamDone {
				m.err = ev.err
			}
			return m, cmd
		default:
			return m, cmd
		}
	case hubStreamClosedMsg:
		return m, nil
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
		ref, err := hubapi.ParseRef(msg.resp.Ref)
		if err != nil {
			m.addSessionSystem("Clear returned invalid ref: " + msg.resp.Ref)
			return m, nil
		}
		return m, fetchHubSession(m.client, ref)
	case sseEventMsg:
		m.applyHubSSEEvent(SSEEvent(msg))
		return m, nil
	case sseErrorMsg:
		if !m.streamDone {
			m.err = msg.err
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
		if m.streamCancel != nil {
			m.streamCancel()
		}
		return m, tea.Quit
	case "q":
		if m.mode == hubModeSession && m.session.scrollMode {
			return m.updateSessionKey(msg)
		}
		if m.streamCancel != nil {
			m.streamCancel()
		}
		return m, tea.Quit
	}
	if m.mode == hubModeSession {
		return m.updateSessionKey(msg)
	}
	if m.mode == hubModeProject {
		return m.updateProjectKey(msg)
	}
	switch msg.String() {
	case "r":
		if m.client != nil {
			return m, fetchHubTree(m.client)
		}
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

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.session.scrollMode {
		switch msg.String() {
		case "esc", "i", "q":
			m.exitSessionBrowse()
		case "up":
			m.session.focusTool(-1)
			m.session.refreshViewport()
			if m.session.focusedToolIdx >= 0 {
				m.session.scrollToMessage(m.session.focusedToolIdx)
			} else {
				m.session.viewport, _ = m.session.viewport.Update(msg)
			}
		case "down":
			m.session.focusTool(1)
			m.session.refreshViewport()
			if m.session.focusedToolIdx >= 0 {
				m.session.scrollToMessage(m.session.focusedToolIdx)
			} else {
				m.session.viewport, _ = m.session.viewport.Update(msg)
			}
		case "tab", "enter":
			if m.session.focusedToolIdx >= 0 && m.session.focusedToolIdx < len(m.session.messages) {
				msg := &m.session.messages[m.session.focusedToolIdx]
				if msg.Kind == msgTool && msg.Tool != nil && msg.Tool.Done {
					msg.Tool.Expanded = !msg.Tool.Expanded
					m.session.refreshViewport()
					m.session.scrollToMessage(m.session.focusedToolIdx)
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
		m.addSessionSystem(slashCommandHelp())
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
	if pageUp {
		m.session.viewport, _ = m.session.viewport.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
}

func (m *hubModel) exitSessionBrowse() {
	m.session.scrollMode = false
	m.session.focusedToolIdx = -1
	m.session.input.Focus()
}

func (m *hubModel) returnToDashboard() {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.mode = hubModeDashboard
	m.clampSelection()
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

func (m *hubModel) addSessionSystem(text string) {
	m.session.messages = append(m.session.messages, chatMessage{Kind: msgSystem, Text: text})
	m.session.refreshViewport()
}

func (m hubModel) currentRef() (hubapi.Ref, bool) {
	ref, err := hubapi.ParseRef(m.detail.Ref)
	if err != nil {
		return hubapi.Ref{}, false
	}
	return ref, true
}

func (m *hubModel) applyHubSSEEvent(ev SSEEvent) {
	if ev.ID != "" {
		m.streamLastID = ev.ID
	}
	if ev.Event == "REPLAY_DONE" {
		m.streamDone = true
		return
	}
	m.session.handleSSEEvent(ev)
	m.session.refreshViewport()
}

func (m hubModel) startSessionStream() tea.Cmd {
	stream := m.detail.Streams.TranscriptFollow
	if stream == "" {
		if m.detail.Live {
			stream = m.detail.Streams.Live
		} else {
			stream = m.detail.Streams.Replay
		}
	}
	if stream == "" || m.client == nil {
		return nil
	}
	return startHubStream(m.client.URL(stream), m.streamLastID)
}

func buildHubRows(tree hubapi.TreeResponse) []hubRow {
	return buildDashboardRows(tree)
}

func buildDashboardRows(tree hubapi.TreeResponse) []hubRow {
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
	addSession := func(key, project string, n hubapi.TreeNode) {
		if !n.Live {
			return
		}
		ref, err := hubapi.ParseRef(n.Ref)
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
			kind:       hubRowSession,
			ref:        ref,
			title:      title,
			project:    project,
			projectKey: key,
			state:      n.State,
			live:       n.Live,
			model:      n.Model,
			age:        n.Age,
			rowID:      rowID,
		})
	}

	for _, p := range tree.Projects {
		var live []hubapi.TreeNode
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

func buildProjectRows(project hubapi.TreeProject) []hubRow {
	var liveRows []hubRow
	var recentRows []hubRow
	key := project.Key
	if key == "" {
		key = hubProjectKey(project.Name)
	}
	add := func(n hubapi.TreeNode) {
		ref, err := hubapi.ParseRef(n.Ref)
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
			kind:       hubRowSession,
			ref:        ref,
			title:      title,
			project:    project.Name,
			projectKey: key,
			state:      state,
			live:       n.Live,
			model:      n.Model,
			age:        n.Age,
			rowID:      rowID,
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

func (m hubModel) selectedProject() (hubapi.TreeProject, bool) {
	for _, p := range m.tree.Projects {
		key := p.Key
		if key == "" {
			key = hubProjectKey(p.Name)
		}
		if key == m.selectedProjectKey {
			return p, true
		}
	}
	return hubapi.TreeProject{}, false
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
		fmt.Fprintf(&b, "%s   %s %-10s %-32s %-10s %s\n", cursor, statusDot(row.state), stateLabel(row.state), row.title, row.model, row.age)
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
	fmt.Fprintf(b, "%s %s %-10s %-34s %-10s %s\n", cursor, statusDot(row.state), stateLabel(row.state), row.title, row.model, row.age)
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

func (m hubModel) sessionView() string {
	var b strings.Builder
	title := m.detail.Title
	if title == "" {
		title = m.detail.SessionID
	}
	fmt.Fprintf(&b, "%s\n", title)
	fmt.Fprintf(&b, "%s  %s  %s\n", m.detail.Ref, m.detail.State, m.detail.WorkingDir)
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
	b.WriteString("\nesc: dashboard  q: quit\n")
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
