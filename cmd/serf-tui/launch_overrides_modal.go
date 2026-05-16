package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

func (m launchOverridesModal) Init() tea.Cmd { return nil }

func (m launchOverridesModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
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
			rows := layerRows(m.cur)
			if m.cursor < len(rows)-1 {
				m.cursor++
			}
		case tea.KeyEnter:
			rows := layerRows(m.cur)
			if m.cursor >= len(rows) {
				return m, nil
			}
			row := rows[m.cursor]
			return m, func() tea.Msg {
				return launchSettingsEditRequestMsg{Layer: "launch", Field: row.field, CurrentValue: row.value}
			}
		}
	}
	return m, nil
}

func (m launchOverridesModal) View() string {
	var b strings.Builder
	b.WriteString("Per-launch overrides for next thread\n\n")
	rows := layerRows(m.cur)
	for i, r := range rows {
		c := "  "
		if i == m.cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	b.WriteString("\n[Enter] edit  [Ctrl-S] save  [Esc] cancel")
	return b.String()
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
