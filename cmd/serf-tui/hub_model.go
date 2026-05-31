package main

import (
	"fmt"
	"sort"
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
	right := fmt.Sprintf("%s · %d live", hubURL, liveCount)
	return SectionDivider(width, "SERF LIVE", right)
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
		b.WriteString(renderDashboardSessionRow(row, i == selected, width, compact, ""))
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

func stateColor(state string) lipgloss.Color {
	th := activeTheme()
	switch state {
	case "awaiting":
		return th.StateAwaiting
	case "active":
		return th.StateProcessing
	case "warning":
		return th.StateWarning
	case "idle":
		return th.StateIdle
	case "ended":
		return th.StateEnded
	default:
		return th.TextDim
	}
}

func renderDashboardSessionRow(row hubRow, selected bool, width int, compact bool, _ string) string {
	// Single-glyph marker either way. FocusedStateBar would render
	// ▍▍ which, after ANSI-stripping for the selected highlight,
	// shifts the row content one cell right on selection. The
	// SurfaceSecondary bg highlight is the selection indicator;
	// the marker stays one cell wide for column stability.
	marker := StateBar(stateColor(row.state))
	styles := defaultTUIStyles()
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
	_ = compact // compact/non-compact share layout today; keep param for the call sites
	// Use ANSI-aware truncation: the joined line carries SGR escapes from
	// StateBar and dashboardCell helpers. truncateText slices raw runes
	// and would chop through escape sequences (a tail-end \x1b[0m or fg
	// switch gets cut, leaking style into the next row), and the selected
	// branch below relies on ANSI being intact before it strips them.
	line = ansi.Truncate(line, width, "")
	if selected {
		// Strip inner ANSI styling so the Selected style's background
		// paints the whole row. Inner styled spans (StateBar, statusDot)
		// emit \x1b[0m resets that would otherwise break the parent's bg
		// after the first colored fragment, leaving most of the row
		// without the highlight. The selection bg itself is now the
		// indicator; inner state colors are not needed on selected rows.
		return styles.Selected.Width(width).Render(ansi.Strip(line))
	}
	if row.state == "awaiting" || row.state == "active" || row.state == "warning" {
		clr := stateColor(row.state)
		line = lipgloss.NewStyle().Foreground(clr).Render(line)
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
	tokens := []string{
		KbdHint("↑↓", "select"),
		KbdHint("enter", "open"),
		KbdHint("n", "new"),
		KbdHint("/", "filter"),
		KbdHint("ctrl+o", "dashboard"),
		KbdHint("q", "quit"),
	}
	return actionBarForWidth(width, tokens...)
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
