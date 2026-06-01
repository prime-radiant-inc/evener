package tuipick

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
)

type PickerPanelItem struct {
	ID             string
	Label          string
	Detail         string
	DisabledReason string
}

type PickerPanel struct {
	title     string
	items     []PickerPanelItem
	filter    string
	cursor    int
	width     int
	selected  string
	cancelled bool
	done      bool
}

func NewPickerPanel(title string, items []PickerPanelItem, width int) PickerPanel {
	if width <= 0 {
		width = 80
	}
	return PickerPanel{title: title, items: items, width: width}
}

func (p PickerPanel) Init() tea.Cmd { return nil }

func (p PickerPanel) filtered() []PickerPanelItem {
	if p.filter == "" {
		return p.items
	}
	lower := strings.ToLower(p.filter)
	var out []PickerPanelItem
	for _, item := range p.items {
		if strings.Contains(strings.ToLower(item.ID), lower) ||
			strings.Contains(strings.ToLower(item.Label), lower) ||
			strings.Contains(strings.ToLower(item.Detail), lower) {
			out = append(out, item)
		}
	}
	return out
}

func (p PickerPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, nil
		case tea.KeyEnter:
			filtered := p.filtered()
			if len(filtered) == 0 || p.cursor >= len(filtered) {
				return p, nil
			}
			item := filtered[p.cursor]
			if item.DisabledReason != "" {
				return p, nil
			}
			p.selected = item.ID
			p.done = true
			return p, nil
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			filtered := p.filtered()
			if p.cursor < len(filtered)-1 {
				p.cursor++
			}
		case tea.KeyBackspace:
			if len(p.filter) > 0 {
				p.filter = p.filter[:len(p.filter)-1]
				p.cursor = 0
			}
		case tea.KeyRunes:
			p.filter += string(msg.Runes)
			p.cursor = 0
		}
	}
	return p, nil
}

// SetFilter sets the current filter string.
func (p *PickerPanel) SetFilter(filter string) { p.filter = filter }

// Done reports whether the panel has been dismissed.
func (p PickerPanel) Done() bool { return p.done }

// Selected returns the chosen item ID, or "" if none was selected.
func (p PickerPanel) Selected() string { return p.selected }

// Cancelled reports whether the panel was dismissed without a selection.
func (p PickerPanel) Cancelled() bool { return p.cancelled }

// Filtered returns the items matching the current filter, in display order.
func (p PickerPanel) Filtered() []PickerPanelItem { return p.filtered() }

// Cursor returns the index of the highlighted row within the filtered list.
func (p PickerPanel) Cursor() int { return p.cursor }

// Filter returns the current filter string.
func (p PickerPanel) Filter() string { return p.filter }

// Width returns the panel's render width.
func (p PickerPanel) Width() int { return p.width }

// Title returns the panel title.
func (p PickerPanel) Title() string { return p.title }

func (p PickerPanel) View() string {
	var b strings.Builder
	title := p.title
	if title == "" {
		title = "Select"
	}
	b.WriteString(title)
	b.WriteString("\n")
	if p.filter == "" {
		b.WriteString("Filter: type to filter...\n\n")
	} else {
		fmt.Fprintf(&b, "Filter: %s\n\n", p.filter)
	}

	filtered := p.filtered()
	if len(filtered) == 0 {
		b.WriteString("  No matching items.\n")
	} else {
		for i, item := range filtered {
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			fmt.Fprintf(&b, "%s%s", cursor, item.Label)
			if item.Detail != "" {
				fmt.Fprintf(&b, "  %s", item.Detail)
			}
			if item.DisabledReason != "" {
				fmt.Fprintf(&b, "  disabled: %s", item.DisabledReason)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString("type filter  up/down navigate  enter select  esc close")
	return tuiprim.RenderPopupPane(b.String(), p.width)
}
