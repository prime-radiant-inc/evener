package main

import (
	"sort"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/hubapi"
)

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
			askPending:  n.AskPending,
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
	aBand, bBand := hubapi.NeedsYouBand(stateLabel(a.state), a.askPending), hubapi.NeedsYouBand(stateLabel(b.state), b.askPending)
	if aBand != bBand {
		return aBand > bBand
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
