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
	hubModeSession
)

type hubRow struct {
	ref     hubapi.Ref
	title   string
	project string
	state   string
	live    bool
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
		m.rows = buildHubRows(msg.tree)
		if m.selected >= len(m.rows) {
			m.selected = len(m.rows) - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
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
	case "ctrl+c", "q":
		if m.streamCancel != nil {
			m.streamCancel()
		}
		return m, tea.Quit
	case "esc", "backspace":
		if m.mode == hubModeSession {
			if m.streamCancel != nil {
				m.streamCancel()
				m.streamCancel = nil
			}
			m.mode = hubModeDashboard
			return m, nil
		}
	}
	if m.mode == hubModeSession {
		return m.updateSessionKey(msg)
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
	case "enter":
		if len(m.rows) > 0 && m.client != nil {
			return m, fetchHubSession(m.client, m.rows[m.selected].ref)
		}
	}
	return m, nil
}

func (m hubModel) updateSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	var rows []hubRow
	seen := map[string]bool{}
	add := func(n hubapi.TreeNode) {
		ref, err := hubapi.ParseRef(n.Ref)
		if err != nil || seen[n.Ref] {
			return
		}
		seen[n.Ref] = true
		rows = append(rows, hubRow{
			ref:     ref,
			title:   n.Title,
			project: n.Project,
			state:   n.State,
			live:    n.Live,
		})
	}
	for _, n := range tree.Live {
		add(n)
	}
	for _, p := range tree.Projects {
		for _, n := range p.Sessions {
			if n.Project == "" {
				n.Project = p.Name
			}
			add(n)
		}
	}
	return rows
}

func (m hubModel) View() string {
	if m.mode == hubModeSession {
		return m.sessionView()
	}
	return m.dashboardView()
}

func (m hubModel) dashboardView() string {
	var b strings.Builder
	fmt.Fprintf(&b, "serf hub\n%s\n\n", m.hubURL)
	if m.err != nil {
		fmt.Fprintf(&b, "error: %v\n\n", m.err)
	}
	if len(m.rows) == 0 {
		b.WriteString("No sessions found.\n")
		return b.String()
	}
	for i, row := range m.rows {
		cursor := " "
		if i == m.selected {
			cursor = ">"
		}
		live := " "
		if row.live {
			live = "*"
		}
		fmt.Fprintf(&b, "%s %s %-10s %-14s %s\n", cursor, live, row.state, row.project, row.title)
	}
	b.WriteString("\nenter: open  r: refresh  q: quit\n")
	return b.String()
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
