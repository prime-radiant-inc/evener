package tuipick

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

var ThemePickerItems = []string{"system", "dark", "light"}

type ThemePicker struct {
	cursor   int
	done     bool
	selected string // set on enter; "" means cancelled
}

func NewThemePicker() ThemePicker {
	p := ThemePicker{}
	// Pre-select cursor to the current theme.
	for i, name := range ThemePickerItems {
		if name == tuitheme.CurrentThemeName() {
			p.cursor = i
			break
		}
	}
	return p
}

func (p ThemePicker) Update(msg tea.Msg) (ThemePicker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			p.done = true
		case tea.KeyEnter:
			p.selected = ThemePickerItems[p.cursor]
			p.done = true
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			if p.cursor < len(ThemePickerItems)-1 {
				p.cursor++
			}
		}
	}
	return p, nil
}

func (p ThemePicker) renderItems() string {
	var b strings.Builder
	for i, name := range ThemePickerItems {
		cursor := "  "
		style := tuitheme.MpNormalStyle
		if i == p.cursor {
			cursor = "> "
			style = tuitheme.MpCursorStyle
		}
		line := cursor + style.Render(name)
		if name == tuitheme.CurrentThemeName() {
			line += "  " + tuitheme.MpActiveTag.Render("(active)")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// Done reports whether the picker has been dismissed.
func (p ThemePicker) Done() bool { return p.done }

// Selected returns the chosen theme name, or "" if cancelled.
func (p ThemePicker) Selected() string { return p.selected }

func (p ThemePicker) View() string {
	width := 44
	body := p.renderItems()
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("enter", "select"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Select theme", Width: width, Body: body, Footer: footer})
}
