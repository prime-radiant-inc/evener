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

func commandPaletteEntriesForRows(mode hubMode, rows []hubRow) []commandPaletteEntry {
	entries := []commandPaletteEntry{{
		Item: pickerPanelItem{
			ID:     "command:new",
			Label:  "New session",
			Detail: "open spawn form",
		},
		Kind:    commandPaletteCommand,
		Command: "new",
	}}
	if mode == hubModeDashboard {
		entries = append(entries, commandPaletteEntry{
			Item: pickerPanelItem{
				ID:     "command:refresh",
				Label:  "Refresh dashboard",
				Detail: "fetch live sessions",
			},
			Kind:    commandPaletteCommand,
			Command: "refresh",
		})
	}
	entries = append(entries, commandPaletteEntry{
		Item: pickerPanelItem{
			ID:             "command:clear",
			Label:          "Clear current session",
			DisabledReason: "open a session first",
		},
		Kind:    commandPaletteCommand,
		Command: "clear",
	})

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
