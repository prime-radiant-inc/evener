package launchconfig

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/internal/appwire"
)

type LaunchOverridesResultMsg struct {
	Overrides *appwire.LaunchConfigLayer
	Cancelled bool
}

type LaunchOverridesOpenMsg struct {
	Initial *appwire.LaunchConfigLayer
}

type LaunchOverridesModal struct {
	cur       appwire.LaunchConfigLayer
	schema    []appwire.LaunchOption
	cursor    int
	done      bool
	cancelled bool
}

func NewLaunchOverridesModal() LaunchOverridesModal {
	return LaunchOverridesModal{}
}

func NewLaunchOverridesModalWith(initial appwire.LaunchConfigLayer) LaunchOverridesModal {
	return LaunchOverridesModal{cur: initial}
}

func NewLaunchOverridesModalWithSchema(initial appwire.LaunchConfigLayer, schema []appwire.LaunchOption) LaunchOverridesModal {
	return LaunchOverridesModal{cur: initial, schema: schema}
}

func (m LaunchOverridesModal) Init() tea.Cmd { return nil }

func (m LaunchOverridesModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case LaunchSchemaResultMsg:
		if v.Err == nil {
			m.schema = v.Schema.Options
		}
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, func() tea.Msg { return LaunchOverridesResultMsg{Cancelled: true} }
		case tea.KeyCtrlS:
			m.done = true
			cp := m.cur
			return m, func() tea.Msg { return LaunchOverridesResultMsg{Overrides: &cp} }
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
				return LaunchSettingsEditRequestMsg{Layer: "launch", Field: row.field, CurrentValue: row.editValue, PathCompletion: row.pathCompletion}
			}
		}
	}
	return m, nil
}

func (m LaunchOverridesModal) renderFields() string {
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

func (m LaunchOverridesModal) View() string {
	body := "Per-launch overrides for next thread\n\n" + m.renderFields()
	width := 80
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("enter", "edit"), tuiprim.KbdHint("ctrl-s", "save"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Launch overrides", Width: width, Body: body, Footer: footer})
}

// ApplyEdit returns a copy of the modal with the field updated. Used by
// the hub model after a textInputModal returns a result.
func (m LaunchOverridesModal) ApplyEdit(field, value string) (LaunchOverridesModal, error) {
	updated, err := applyEdit(m.cur, field, value)
	if err != nil {
		return m, err
	}
	m.cur = updated
	return m, nil
}

func (m LaunchOverridesModal) Current() appwire.LaunchConfigLayer { return m.cur }

// Done reports whether the modal has been dismissed (committed or cancelled).
func (m LaunchOverridesModal) Done() bool { return m.done }

func (m LaunchOverridesModal) rows() []layerRow {
	if len(m.schema) > 0 {
		return launchSchemaRows(m.schema, m.cur, launchLayerLaunch, launchSchemaRowsOverride)
	}
	return layerRows(m.cur)
}
