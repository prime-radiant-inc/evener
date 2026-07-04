package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitext"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

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
	topBar := dashboardHeader(m.hubURL, liveCount, width, needsYouBadge(needsYouCount(m.rows)))
	var b strings.Builder
	if m.err != nil {
		b.WriteString(tuitext.TruncateText(fmt.Sprintf("error: %v", m.err), width))
		b.WriteString("\n\n")
	}
	if notices := m.renderNotices(); notices != "" {
		b.WriteString(notices)
		b.WriteString("\n")
	}
	if m.commandPalette != nil {
		footer := tuiprim.ActionBarForWidth(m.width, "up/down select", "enter open/toggle", "n new", "/ palette", "ctrl+o dashboard", "q quit")
		overlayHeight := paletteOverlayHeight(m.height, topBar, b.String(), footer)
		return tuiprim.AppShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.commandPalette.ViewWithMaxHeight(overlayHeight),
			Footer:  footer,
			Height:  m.height,
		}.View()
	}
	if m.followupModal != nil {
		return tuiprim.AppShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.followupModal.View(),
			Footer:  "[Enter] confirm  [Esc] cancel",
			Height:  m.height,
		}.View()
	}
	if m.credentialsPanel != nil {
		return tuiprim.AppShell{
			TopBar:  topBar,
			Body:    b.String(),
			Overlay: m.credentialsPanel.View(),
			Footer:  "[Enter] set api key  [O] OAuth sign-in  [C] clear  [Esc] close",
			Height:  m.height,
		}.View()
	}
	if m.launchSettingsPanel != nil {
		return tuiprim.AppShell{
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
			return tuiprim.AppShell{
				TopBar: topBar,
				Body:   b.String(),
				Footer: "esc clear filter",
				Height: m.height,
			}.View()
		}
		b.WriteString("No live sessions are running.\n\n")
		return tuiprim.AppShell{
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
		drawer := tuitext.LimitFirstLines(m.dashboardDetailsView(rows, drawerWidth), rowLimit)
		b.WriteString(joinDashboardColumns(list, drawer, listWidth, drawerWidth, width))
	} else {
		b.WriteString(renderDashboardRowsWindow(rows, m.selected, width, width <= 72, rowLimit))
	}
	b.WriteString("\n")
	return tuiprim.AppShell{
		TopBar: topBar,
		Body:   b.String(),
		Footer: footer,
		Height: m.height,
	}.View()
}

// paletteOverlayHeight returns the rows available for the command-palette
// overlay between the anchored TopBar and Footer, mirroring dashboardRowLimit so
// the overlay windows itself instead of overflowing a short pane. A zero or
// negative total height means "unbounded" and yields 0.
func paletteOverlayHeight(totalHeight int, topBar, bodyPrefix, footer string) int {
	if totalHeight <= 0 {
		return 0
	}
	height := sessionShellBodyHeight(totalHeight, topBar, "", footer)
	height -= tuitext.ShellSectionLineCount(bodyPrefix)
	if height < 1 {
		return 1
	}
	return height
}

func (m hubModel) dashboardUsesWideLayout() bool {
	return m.width >= 120 && m.commandPalette == nil
}

// dashboardHeader renders the dashboard's one-line top bar. badge is the
// pre-rendered needs-you indicator (needsYouBadge; empty when quiet) and is
// folded into the divider's right-side content: SectionDivider fills to
// exactly width columns, so appending anything after it would wrap the
// TopBar and break AppShell's single-line height accounting.
func dashboardHeader(hubURL string, liveCount int, width int, badge string) string {
	right := fmt.Sprintf("%s · %d live", hubURL, liveCount) + badge
	return tuiprim.SectionDivider(width, "SERF LIVE", right)
}

// needsYouCount reports how many live sessions are in a state that needs the
// user's attention (awaiting input, warning, or errored). Only hubRowSession
// rows are counted; a project's rollup row carries the same aggregated state
// as its children (see buildDashboardRows) and would double-count otherwise.
func needsYouCount(rows []hubRow) int {
	count := 0
	for _, row := range rows {
		if row.kind != hubRowSession || !row.live {
			continue
		}
		switch stateLabel(row.state) {
		case "awaiting", "warning", "errored":
			count++
		}
	}
	return count
}

// needsYouBadge renders the "◆N" needs-you indicator in the awaiting-attention
// color, folded into the dashboard header divider's right-side content (with
// its own leading separator space). Empty when n is zero so the header stays
// quiet when nothing needs the user.
func needsYouBadge(n int) string {
	if n <= 0 {
		return ""
	}
	return " " + lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateAwaiting).Render(fmt.Sprintf("◆%d", n))
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
	line := tuitext.TruncateText(cursor+" + "+row.title, width)
	if selected {
		return tuitheme.DefaultTUIStyles().Selected.Render(line)
	}
	return line
}

func dashboardRowLimit(totalHeight int, topBar string, bodyPrefix string, footer string) int {
	if totalHeight <= 0 {
		return 0
	}
	limit := sessionShellBodyHeight(totalHeight, topBar, "", footer)
	limit -= tuitext.ShellSectionLineCount(bodyPrefix)
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
	styles := tuitheme.DefaultTUIStyles()
	line := fmt.Sprintf("%s %s %s %s  %s", cursor, marker, statusDot(row.state), dashboardCell(row.project), projectSummary(row, rows))
	line = tuitext.TruncateText(line, width)
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
	line := tuitext.TruncateText(fmt.Sprintf("%s %s %s %d %s", cursor, marker, dashboardCell(row.project), count, label), width)
	if selected {
		return tuitheme.DefaultTUIStyles().Selected.Render(line)
	}
	return tuitheme.DefaultTUIStyles().Muted.Render(line)
}

func stateColor(state string) lipgloss.Color {
	th := tuitheme.ActiveTheme()
	// Switch on the normalized label, not the raw value: hubRow.state can
	// carry a raw wire status (e.g. "systemError", "closed") rather than the
	// hub-normalized form, mirroring statusDot/attentionRankLabel below.
	switch stateLabel(state) {
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
	case "errored":
		return th.StateError
	default:
		return th.TextDim
	}
}

func renderDashboardSessionRow(row hubRow, selected bool, width int, compact bool, _ string) string {
	// Single-glyph marker either way. tuiprim.FocusedStateBar would render
	// ▍▍ which, after ANSI-stripping for the selected highlight,
	// shifts the row content one cell right on selection. The
	// SurfaceSecondary bg highlight is the selection indicator;
	// the marker stays one cell wide for column stability.
	marker := tuiprim.StateBar(stateColor(row.state))
	styles := tuitheme.DefaultTUIStyles()
	line := strings.Join(tuitext.NonEmptyStrings([]string{
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
	// tuiprim.StateBar and dashboardCell helpers. tuitext.TruncateText slices raw runes
	// and would chop through escape sequences (a tail-end \x1b[0m or fg
	// switch gets cut, leaking style into the next row), and the selected
	// branch below relies on ANSI being intact before it strips them.
	line = ansi.Truncate(line, width, "")
	if selected {
		// Strip inner ANSI styling so the Selected style's background
		// paints the whole row. Inner styled spans (tuiprim.StateBar, statusDot)
		// emit \x1b[0m resets that would otherwise break the parent's bg
		// after the first colored fragment, leaving most of the row
		// without the highlight. The selection bg itself is now the
		// indicator; inner state colors are not needed on selected rows.
		return styles.Selected.Width(width).Render(ansi.Strip(line))
	}
	switch stateLabel(row.state) {
	case "awaiting", "active", "warning", "errored":
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
		tuiprim.KbdHint("↑↓", "select"),
		tuiprim.KbdHint("enter", "open"),
		tuiprim.KbdHint("n", "new"),
		tuiprim.KbdHint("/", "filter"),
		tuiprim.KbdHint("ctrl+o", "dashboard"),
		tuiprim.KbdHint("q", "quit"),
	}
	return tuiprim.ActionBarForWidth(width, tokens...)
}

func emptyDashboardFooter(width int) string {
	items := []string{"n new session"}
	items = append(items, "/ palette", "q quit")
	if width <= 72 {
		return strings.Join(items, "\n")
	}
	return strings.Join(items, "  ")
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
	return tuiprim.RenderStyledPane(text, width)
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
	case "errored":
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
	case "systemerror", "errored":
		return "errored"
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
	case "errored":
		return 5
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
