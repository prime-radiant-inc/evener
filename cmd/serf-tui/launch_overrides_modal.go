package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/internal/appwire"
)

type launchOverridesResultMsg struct {
	Overrides *appwire.LaunchConfigLayer
	Cancelled bool
}

type launchOverridesOpenMsg struct {
	Initial *appwire.LaunchConfigLayer
}

type launchOverridesModal struct {
	cur       appwire.LaunchConfigLayer
	schema    []appwire.LaunchOption
	cursor    int
	done      bool
	cancelled bool
}

func newLaunchOverridesModal() launchOverridesModal {
	return launchOverridesModal{}
}

func newLaunchOverridesModalWith(initial appwire.LaunchConfigLayer) launchOverridesModal {
	return launchOverridesModal{cur: initial}
}

func newLaunchOverridesModalWithSchema(initial appwire.LaunchConfigLayer, schema []appwire.LaunchOption) launchOverridesModal {
	return launchOverridesModal{cur: initial, schema: schema}
}

func (m launchOverridesModal) Init() tea.Cmd { return nil }

func (m launchOverridesModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case launchSchemaResultMsg:
		if v.Err == nil {
			m.schema = v.Schema.Options
		}
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, func() tea.Msg { return launchOverridesResultMsg{Cancelled: true} }
		case tea.KeyCtrlS:
			m.done = true
			cp := m.cur
			return m, func() tea.Msg { return launchOverridesResultMsg{Overrides: &cp} }
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			rows := m.rows()
			if m.cursor < len(rows)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			rows := m.rows()
			if m.cursor >= len(rows) {
				return m, nil
			}
			row := rows[m.cursor]
			if launchSettingsFieldReadOnly(row.field) {
				return m, nil
			}
			return m, func() tea.Msg {
				return launchSettingsEditRequestMsg{Layer: "launch", Field: row.field, CurrentValue: row.editValue, PathCompletion: row.pathCompletion}
			}
		}
	}
	return m, nil
}

func (m launchOverridesModal) renderFields() string {
	var b strings.Builder
	rows := m.rows()
	for i, r := range rows {
		c := "  "
		if i == m.cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	return b.String()
}

func (m launchOverridesModal) View() string {
	body := "Per-launch overrides for next thread\n\n" + m.renderFields()
	width := 80
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("enter", "edit"), tuiprim.KbdHint("ctrl-s", "save"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Launch overrides", Width: width, Body: body, Footer: footer})
}

// ApplyEdit returns a copy of the modal with the field updated. Used by
// the hub model after a textInputModal returns a result.
func (m launchOverridesModal) ApplyEdit(field, value string) (launchOverridesModal, error) {
	updated, err := applyEdit(m.cur, field, value)
	if err != nil {
		return m, err
	}
	m.cur = updated
	return m, nil
}

func (m launchOverridesModal) Current() appwire.LaunchConfigLayer { return m.cur }

func (m launchOverridesModal) rows() []layerRow {
	if len(m.schema) > 0 {
		return launchSchemaRows(m.schema, m.cur, launchLayerLaunch, launchSchemaRowsOverride)
	}
	return layerRows(m.cur)
}
