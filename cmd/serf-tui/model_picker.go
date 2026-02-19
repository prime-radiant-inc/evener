package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type modelPickerItem struct {
	id      string
	display string
}

// modelPicker is an inline Bubble Tea model for selecting from a list of models.
type modelPicker struct {
	items     []modelPickerItem
	active    string // currently active model (highlighted differently)
	filter    string
	cursor    int
	width     int
	selected  string // set on enter
	cancelled bool   // set on esc
	done      bool
}

func newModelPicker(items []modelPickerItem, activeModel string, width int) modelPicker {
	return modelPicker{
		items:  items,
		active: activeModel,
		width:  width,
	}
}

func (m modelPicker) Init() tea.Cmd { return nil }

func (m modelPicker) filtered() []modelPickerItem {
	if m.filter == "" {
		return m.items
	}
	lower := strings.ToLower(m.filter)
	var out []modelPickerItem
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.id), lower) ||
			strings.Contains(strings.ToLower(item.display), lower) {
			out = append(out, item)
		}
	}
	return out
}

func (m modelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, nil
		case tea.KeyEnter:
			filtered := m.filtered()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.selected = filtered[m.cursor].id
			}
			m.done = true
			return m, nil
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			filtered := m.filtered()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case tea.KeyBackspace:
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.cursor = 0
			}
		case tea.KeyRunes:
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m modelPicker) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	filterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	activeTag := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	b.WriteString(titleStyle.Render("Select model"))
	b.WriteString("\n")

	filterText := m.filter
	if filterText == "" {
		filterText = dimStyle.Render("type to filter...")
	} else {
		filterText = filterStyle.Render(filterText)
	}
	b.WriteString(fmt.Sprintf("Filter: %s", filterText))
	b.WriteString("\n\n")

	filtered := m.filtered()
	if len(filtered) == 0 {
		b.WriteString(dimStyle.Render("  No matching models."))
		b.WriteString("\n")
	} else {
		maxVisible := 15
		start := 0
		if len(filtered) > maxVisible {
			start = m.cursor - maxVisible/2
			if start < 0 {
				start = 0
			}
			if start+maxVisible > len(filtered) {
				start = len(filtered) - maxVisible
			}
		}
		end := start + maxVisible
		if end > len(filtered) {
			end = len(filtered)
		}

		for i := start; i < end; i++ {
			item := filtered[i]
			cursor := "  "
			style := normalStyle
			if i == m.cursor {
				cursor = "> "
				style = cursorStyle
			} else if item.id == m.active {
				style = activeStyle
			}
			line := cursor + style.Render(item.display)
			if item.id != item.display && item.display != "" {
				line += "  " + dimStyle.Render(item.id)
			}
			if item.id == m.active {
				line += "  " + activeTag.Render("(active)")
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		if len(filtered) > maxVisible {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ... %d models total", len(filtered))))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ navigate  enter select  esc cancel"))
	return b.String()
}
