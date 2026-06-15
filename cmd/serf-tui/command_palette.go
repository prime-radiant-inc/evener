package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type commandPaletteEntryKind int

const (
	commandPaletteCommand commandPaletteEntryKind = iota
	commandPaletteSession
	commandPaletteProject
)

type commandPaletteEntry struct {
	Item       tuipick.PickerPanelItem
	Kind       commandPaletteEntryKind
	Command    string
	Ref        appwire.Ref
	ProjectKey string
}

type commandPalette struct {
	panel   tuipick.PickerPanel
	entries []commandPaletteEntry
}

func newCommandPalette(title string, entries []commandPaletteEntry, width int) commandPalette {
	items := make([]tuipick.PickerPanelItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Item)
	}
	return commandPalette{panel: tuipick.NewPickerPanel(title, items, width), entries: entries}
}

func (p commandPalette) Init() tea.Cmd { return nil }

func (p commandPalette) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := p.panel.Update(msg)
	panel := updated.(tuipick.PickerPanel)
	p.panel = panel
	return p, cmd
}

// paletteOverlayChrome is the number of rows tuiprim.Overlay adds around the
// item list: rounded border (2), vertical padding (2), title + blank (2),
// filter + blank (2), and blank + footer (2).
const paletteOverlayChrome = 10

// renderItemsWindow renders the filtered items, showing at most maxRows of them
// windowed around the cursor so the selected entry stays visible. maxRows <= 0
// shows every item.
func (p commandPalette) renderItemsWindow(maxRows int) string {
	th := tuitheme.ActiveTheme()
	filtered := p.panel.Filtered()
	if len(filtered) == 0 {
		return lipgloss.NewStyle().Foreground(th.TextDim).Render("  No matching commands.")
	}
	start, end := paletteItemWindow(len(filtered), p.panel.Cursor(), maxRows)
	var rows []string
	for i := start; i < end; i++ {
		item := filtered[i]
		cursor := "  "
		if i == p.panel.Cursor() {
			cursor = "> "
		}
		slash := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render(item.Label)
		detail := lipgloss.NewStyle().Foreground(th.TextDim).Render(item.Detail)
		row := cursor + slash
		if item.Detail != "" {
			row += "  " + detail
		}
		if item.DisabledReason != "" {
			row += "  " + lipgloss.NewStyle().Foreground(th.TextDim).Render("disabled: "+item.DisabledReason)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (p commandPalette) View() string {
	return p.ViewWithMaxHeight(0)
}

// ViewWithMaxHeight renders the palette overlay, windowing its item list so the
// whole box fits within maxHeight rows while keeping the selected entry in view.
// maxHeight <= 0 renders every item.
func (p commandPalette) ViewWithMaxHeight(maxHeight int) string {
	th := tuitheme.ActiveTheme()
	var filterLine string
	if p.panel.Filter() == "" {
		filterLine = "Filter: " + lipgloss.NewStyle().Foreground(th.TextDim).Render("type to filter...")
	} else {
		filterLine = "Filter: " + p.panel.Filter()
	}
	itemRows := 0
	if maxHeight > 0 {
		itemRows = maxHeight - paletteOverlayChrome
		if itemRows < 1 {
			itemRows = 1
		}
	}
	body := filterLine + "\n\n" + p.renderItemsWindow(itemRows)
	width := p.panel.Width()
	if width <= 0 {
		width = 80
	}
	width = min(max(width, 44), 96)
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("enter", "run"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: p.panel.Title(), Width: width, Body: body, Footer: footer})
}

// paletteItemWindow centers a window of at most maxRows items on the cursor,
// mirroring dashboardRowWindow so the selected entry stays visible.
func paletteItemWindow(count, cursor, maxRows int) (int, int) {
	if count <= 0 {
		return 0, 0
	}
	if maxRows <= 0 || maxRows >= count {
		return 0, count
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= count {
		cursor = count - 1
	}
	start := cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start+maxRows > count {
		start = count - maxRows
	}
	return start, start + maxRows
}

func (p commandPalette) selectedEntry() (commandPaletteEntry, bool) {
	if p.panel.Selected() == "" {
		return commandPaletteEntry{}, false
	}
	for _, entry := range p.entries {
		if entry.Item.ID == p.panel.Selected() {
			return entry, true
		}
	}
	return commandPaletteEntry{}, false
}

func commandPaletteEntriesForRows(mode hubMode, caps hubSessionCapabilities, rows []hubRow) []commandPaletteEntry {
	scope := hubCommandDashboard
	if mode == hubModeSession {
		scope = hubCommandSession
	}
	ctx := hubCommandContext{mode: mode, caps: caps}
	entries := make([]commandPaletteEntry, 0, len(rows)+len(hubCommandRegistry))
	for _, command := range hubCommandsForScope(scope) {
		available, reason := hubCommandAvailable(command, ctx)
		item := tuipick.PickerPanelItem{
			ID:             "command:" + command.Name,
			Label:          command.PaletteLabel,
			Detail:         command.PaletteDetail,
			DisabledReason: reason,
		}
		if !available && item.DisabledReason == "" {
			item.DisabledReason = "not available"
		}
		entries = append(entries, commandPaletteEntry{
			Item:    item,
			Kind:    commandPaletteCommand,
			Command: command.Name,
		})
	}

	seenProjects := map[string]bool{}
	for _, row := range rows {
		switch row.kind {
		case hubRowProject:
			if row.projectKey == "" || seenProjects[row.projectKey] {
				continue
			}
			seenProjects[row.projectKey] = true
			entries = append(entries, commandPaletteEntry{
				Item: tuipick.PickerPanelItem{
					ID:     "project:" + row.projectKey,
					Label:  "Project: " + row.project,
					Detail: projectSummary(row, rows),
				},
				Kind:       commandPaletteProject,
				ProjectKey: row.projectKey,
			})
		case hubRowSession:
			ref := row.ref
			refText := ref.String()
			if refText == ":" {
				refText = ""
			}
			detail := strings.TrimSpace(fmt.Sprintf("%s %s %s", row.sourceLabel, stateLabel(row.state), row.model))
			entries = append(entries, commandPaletteEntry{
				Item: tuipick.PickerPanelItem{
					ID:     "session:" + refText,
					Label:  row.title,
					Detail: detail,
				},
				Kind: commandPaletteSession,
				Ref:  ref,
			})
		}
	}
	return entries
}
