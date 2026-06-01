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

func (p commandPalette) renderItems() string {
	th := tuitheme.ActiveTheme()
	filtered := p.panel.Filtered()
	if len(filtered) == 0 {
		return lipgloss.NewStyle().Foreground(th.TextDim).Render("  No matching commands.")
	}
	var rows []string
	for i, item := range filtered {
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
	th := tuitheme.ActiveTheme()
	var filterLine string
	if p.panel.Filter() == "" {
		filterLine = "Filter: " + lipgloss.NewStyle().Foreground(th.TextDim).Render("type to filter...")
	} else {
		filterLine = "Filter: " + p.panel.Filter()
	}
	body := filterLine + "\n\n" + p.renderItems()
	width := p.panel.Width()
	if width <= 0 {
		width = 80
	}
	width = min(max(width, 44), 96)
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("enter", "run"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: p.panel.Title(), Width: width, Body: body, Footer: footer})
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
