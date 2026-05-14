package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type commandPaletteEntryKind int

const (
	commandPaletteCommand commandPaletteEntryKind = iota
	commandPaletteSession
	commandPaletteProject
)

type commandPaletteEntry struct {
	Item       pickerPanelItem
	Kind       commandPaletteEntryKind
	Command    string
	Ref        appwire.Ref
	ProjectKey string
}

type commandPalette struct {
	panel   pickerPanel
	entries []commandPaletteEntry
}

func newCommandPalette(title string, entries []commandPaletteEntry, width int) commandPalette {
	items := make([]pickerPanelItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Item)
	}
	return commandPalette{panel: newPickerPanel(title, items, width), entries: entries}
}

func (p commandPalette) Init() tea.Cmd { return nil }

func (p commandPalette) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := p.panel.Update(msg)
	panel := updated.(pickerPanel)
	p.panel = panel
	return p, cmd
}

func (p commandPalette) View() string {
	return p.panel.View()
}

func (p commandPalette) selectedEntry() (commandPaletteEntry, bool) {
	if p.panel.selected == "" {
		return commandPaletteEntry{}, false
	}
	for _, entry := range p.entries {
		if entry.Item.ID == p.panel.selected {
			return entry, true
		}
	}
	return commandPaletteEntry{}, false
}

func commandPaletteEntriesForRows(mode hubMode, caps hubSessionCapabilities, rows []hubRow) []commandPaletteEntry {
	scope := hubCommandDashboard
	if mode == hubModeProject {
		scope = hubCommandProject
	} else if mode == hubModeSession {
		scope = hubCommandSession
	}
	ctx := hubCommandContext{mode: mode, caps: caps}
	entries := make([]commandPaletteEntry, 0, len(rows)+len(hubCommandRegistry))
	for _, command := range hubCommandsForScope(scope) {
		available, reason := hubCommandAvailable(command, ctx)
		item := pickerPanelItem{
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
				Item: pickerPanelItem{
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
				Item: pickerPanelItem{
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
