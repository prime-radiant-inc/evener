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
	case "r":
		if m.mode == hubModeDashboard && m.client != nil {
			return m, fetchHubTree(m.client)
		}
	case "up", "k":
		if m.mode == hubModeDashboard && m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.mode == hubModeDashboard && m.selected < len(m.rows)-1 {
			m.selected++
		}
	case "enter":
		if m.mode == hubModeDashboard && len(m.rows) > 0 && m.client != nil {
			return m, fetchHubSession(m.client, m.rows[m.selected].ref)
		}
	}
	return m, nil
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
	return b.String()
}
