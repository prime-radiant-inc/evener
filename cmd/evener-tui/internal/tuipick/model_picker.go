package tuipick

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

type ModelPickerItem struct {
	ID             string
	Display        string
	DisabledReason string
	// Group labels the item's section for a browsable, provider-grouped
	// picker ("Recent", a provider name, ...); "" renders no header. Set only
	// by the model-picker path (hub_commands.go); zero-value for
	// NewTranscriptPicker/NewActionPicker leaves their rendering unchanged.
	Group string
	// Meta is a compact trailing tail (context window, price, capability
	// flags) appended dim after the row. "" renders nothing extra.
	Meta string
	// Warnings are the registry's resolved-row notes (e.g. a global-only
	// model under a regional Vertex location). Each renders as its own dim
	// line under the row; the row stays selectable, matching the web and
	// mobile pickers. Empty renders nothing extra.
	Warnings []string
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
	if msg, ok := msg.(tea.KeyMsg); ok {
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
	b.WriteString("Filter: ")
	b.WriteString(filterText)
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
		start, end := m.visibleRange(filtered)
		for i := start; i < end; i++ {
			for _, line := range m.itemLines(filtered, i) {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}

		if start > 0 || end < len(filtered) {
			b.WriteString(tuitheme.MpDimStyle.Render(fmt.Sprintf("  ... %d items total", len(filtered))))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// maxVisibleLines is the picker body's rendered-line budget. Items are not a
// fixed height — each warning adds a line under its row, a group's first item
// adds a header, and the frame wraps anything longer than it is wide — so the
// window is measured in terminal lines rather than items.
const maxVisibleLines = 15

// itemLines is the body an item renders: the group header when it starts a
// group, its row, then one line per warning. The window measurer and the
// renderer share it, so what a row costs is stated once.
func (m ModelPicker) itemLines(filtered []ModelPickerItem, i int) []string {
	item := filtered[i]
	var lines []string
	if item.Group != "" && (i == 0 || filtered[i-1].Group != item.Group) {
		lines = append(lines, tuitheme.MpDimStyle.Render(strings.ToUpper(item.Group)))
	}
	cursor := "  "
	style := tuitheme.MpNormalStyle
	isActive := modelIDMatchesActive(item.ID, m.active)
	if i == m.cursor {
		cursor = "> "
		style = tuitheme.MpCursorStyle
	} else if isActive {
		style = tuitheme.MpActiveStyle
	}
	line := cursor + style.Render(item.Display)
	if item.ID != item.Display && item.Display != "" {
		line += "  " + tuitheme.MpDimStyle.Render(item.ID)
	}
	if item.Meta != "" {
		line += "  " + tuitheme.MpDimStyle.Render(item.Meta)
	}
	if isActive {
		line += "  " + tuitheme.MpActiveTag.Render("(active)")
	}
	if item.DisabledReason != "" {
		line += "  " + tuitheme.MpDimStyle.Render("disabled: "+item.DisabledReason)
	}
	lines = append(lines, line)
	for _, warning := range item.Warnings {
		lines = append(lines, "    "+tuitheme.MpDimStyle.Render("⚠ "+warning))
	}
	return lines
}

// renderedLines is how many terminal lines filtered[i] takes on screen: the
// lines it renders, wrapped the way the overlay's frame wraps the body, since
// a row or warning longer than the frame occupies more than one.
func (m ModelPicker) renderedLines(filtered []ModelPickerItem, i int) int {
	block := strings.Join(m.itemLines(filtered, i), "\n")
	return strings.Count(ansi.Wrap(block, tuiprim.OverlayContentWidth(m.overlayWidth()), ""), "\n") + 1
}

// visibleRange is the [start, end) window of filtered items to render: the
// cursor's item plus the neighbours that fit the budget, taken from both sides
// so the cursor stays near the middle. An item that alone exceeds the budget
// still renders, since a row cannot be shown in part.
func (m ModelPicker) visibleRange(filtered []ModelPickerItem) (int, int) {
	if len(filtered) == 0 {
		return 0, 0
	}
	// A cursor can outlive the list it indexes (a filter narrowed the list, or
	// a caller set it directly), so clamp it rather than trust it.
	cursor := min(max(m.cursor, 0), len(filtered)-1)
	start, end := cursor, cursor+1
	used := m.renderedLines(filtered, cursor)
	for {
		grew := false
		if end < len(filtered) {
			if n := m.renderedLines(filtered, end); used+n <= maxVisibleLines {
				used += n
				end++
				grew = true
			}
		}
		if start > 0 {
			if n := m.renderedLines(filtered, start-1); used+n <= maxVisibleLines {
				start--
				used += n
				grew = true
			}
		}
		if !grew {
			return start, end
		}
	}
}

// modelIDMatchesActive reports whether a picker item ID names the same model
// as the active model, tolerating the provider-qualified "provider/model"
// item ID format (buildModelPickerItems) against a bare active model name
// (hubSessionDetail.Model — the daemon's status.Model, no provider prefix).
func modelIDMatchesActive(id, active string) bool {
	if active == "" {
		return id == ""
	}
	if id == active {
		return true
	}
	if _, model, ok := strings.Cut(id, "/"); ok && model == active {
		return true
	}
	return false
}

// SetTitle overrides the picker's heading.
func (m *ModelPicker) SetTitle(title string) { m.title = title }

// Done reports whether the picker has been dismissed.
func (m ModelPicker) Done() bool { return m.done }

// Selected returns the chosen item ID, or "" if none was selected.
func (m ModelPicker) Selected() string { return m.selected }

// Cancelled reports whether the picker was dismissed without selecting an item.
func (m ModelPicker) Cancelled() bool { return m.cancelled }

func (m ModelPicker) View() string {
	title := m.title
	if title == "" {
		title = "Select model"
	}
	w := m.overlayWidth()
	body := m.renderBody()
	footer := tuiprim.ActionBarForWidth(w, tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("enter", "select"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{
		Title:  title,
		Width:  w,
		Body:   body,
		Footer: footer,
	})
}

// overlayWidth is the width of the frame the picker renders into: the old
// tuiprim.RenderPopupPane logic, min(max(termWidth, 44), 96), so content at 90
// chars is not word-wrapped by the Overlay frame.
func (m ModelPicker) overlayWidth() int {
	w := m.width
	if w <= 0 {
		w = 96
	}
	return min(max(w, 44), 96)
}
