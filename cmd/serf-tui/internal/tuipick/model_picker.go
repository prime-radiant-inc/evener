package tuipick

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type ModelPickerItem struct {
	ID             string
	Display        string
	DisabledReason string
}

// ModelPicker is an inline Bubble Tea model for selecting from a filtered list.
type ModelPicker struct {
	title     string
	emptyText string
	footer    string
	items     []ModelPickerItem
	active    string // currently active model (highlighted differently)
	filter    string
	cursor    int
	width     int
	selected  string // set on enter
	cancelled bool   // set on esc
	done      bool
}

func NewModelPicker(items []ModelPickerItem, activeModel string, width int) ModelPicker {
	return ModelPicker{
		title:     "Select model",
		emptyText: "  No matching models.",
		footer:    "up/down navigate  enter select  esc cancel",
		items:     items,
		active:    activeModel,
		width:     width,
	}
}

func NewTranscriptPicker(items []ModelPickerItem, activeSessionID string, width int) ModelPicker {
	return ModelPicker{
		title:     "Select transcript",
		emptyText: "  No matching sessions.",
		footer:    "up/down navigate  enter select  esc cancel",
		items:     items,
		active:    activeSessionID,
		width:     width,
	}
}

func NewActionPicker(title, footer string, items []ModelPickerItem, width int) ModelPicker {
	return ModelPicker{
		title:     title,
		emptyText: "  No actions available.",
		footer:    footer,
		items:     items,
		width:     width,
	}
}

func (m ModelPicker) Init() tea.Cmd { return nil }

func (m ModelPicker) filtered() []ModelPickerItem {
	if m.filter == "" {
		return m.items
	}
	lower := strings.ToLower(m.filter)
	var out []ModelPickerItem
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.ID), lower) ||
			strings.Contains(strings.ToLower(item.Display), lower) ||
			strings.Contains(strings.ToLower(item.DisabledReason), lower) {
			out = append(out, item)
		}
	}
	return out
}

func (m ModelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, nil
		case tea.KeyEnter:
			filtered := m.filtered()
			if len(filtered) == 0 || m.cursor >= len(filtered) {
				return m, nil
			}
			item := filtered[m.cursor]
			if item.DisabledReason != "" {
				return m, nil
			}
			m.selected = item.ID
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

func (m ModelPicker) renderBody() string {
	var b strings.Builder

	filterText := m.filter
	if filterText == "" {
		filterText = tuitheme.MpDimStyle.Render("type to filter...")
	} else {
		filterText = tuitheme.MpFilterStyle.Render(filterText)
	}
	b.WriteString(fmt.Sprintf("Filter: %s", filterText))
	b.WriteString("\n\n")

	filtered := m.filtered()
	if len(filtered) == 0 {
		emptyText := m.emptyText
		if emptyText == "" {
			emptyText = "  No matching items."
		}
		b.WriteString(tuitheme.MpDimStyle.Render(emptyText))
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
			style := tuitheme.MpNormalStyle
			if i == m.cursor {
				cursor = "> "
				style = tuitheme.MpCursorStyle
			} else if item.ID == m.active {
				style = tuitheme.MpActiveStyle
			}
			line := cursor + style.Render(item.Display)
			if item.ID != item.Display && item.Display != "" {
				line += "  " + tuitheme.MpDimStyle.Render(item.ID)
			}
			if item.ID == m.active {
				line += "  " + tuitheme.MpActiveTag.Render("(active)")
			}
			if item.DisabledReason != "" {
				line += "  " + tuitheme.MpDimStyle.Render("disabled: "+item.DisabledReason)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		if len(filtered) > maxVisible {
			b.WriteString(tuitheme.MpDimStyle.Render(fmt.Sprintf("  ... %d items total", len(filtered))))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// SetTitle overrides the picker's heading.
func (m *ModelPicker) SetTitle(title string) { m.title = title }

// Done reports whether the picker has been dismissed.
func (m ModelPicker) Done() bool { return m.done }

// Selected returns the chosen item ID, or "" if none was selected.
func (m ModelPicker) Selected() string { return m.selected }

func (m ModelPicker) View() string {
	title := m.title
	if title == "" {
		title = "Select model"
	}
	// Match old tuiprim.RenderPopupPane width logic: popup is min(max(termWidth,44),96)
	// so content at 90 chars won't be word-wrapped by the Overlay frame.
	w := m.width
	if w <= 0 {
		w = 96
	}
	w = min(max(w, 44), 96)
	body := m.renderBody()
	footer := tuiprim.ActionBarForWidth(w, tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("enter", "select"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{
		Title:  title,
		Width:  w,
		Body:   body,
		Footer: footer,
	})
}
