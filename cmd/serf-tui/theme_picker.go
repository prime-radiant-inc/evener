package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var themePickerItems = []string{"system", "dark", "light"}

type themePicker struct {
	cursor    int
	done      bool
	selected  string // set on enter; "" means cancelled
}

func newThemePicker() themePicker {
	p := themePicker{}
	// Pre-select cursor to the current theme.
	for i, name := range themePickerItems {
		if name == currentThemeName() {
			p.cursor = i
			break
		}
	}
	return p
}

func (p themePicker) Update(msg tea.Msg) (themePicker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			p.done = true
		case tea.KeyEnter:
			p.selected = themePickerItems[p.cursor]
			p.done = true
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			if p.cursor < len(themePickerItems)-1 {
				p.cursor++
			}
		}
	}
	return p, nil
}

func (p themePicker) View() string {
	var b strings.Builder

	b.WriteString(mpTitleStyle.Render("Select theme"))
	b.WriteString("\n\n")

	for i, name := range themePickerItems {
		cursor := "  "
		style := mpNormalStyle
		if i == p.cursor {
			cursor = "> "
			style = mpCursorStyle
		}
		line := cursor + style.Render(name)
		if name == currentThemeName() {
			line += "  " + mpActiveTag.Render("(active)")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(mpDimStyle.Render("↑/↓ navigate  enter select  esc cancel"))
	return b.String()
}
