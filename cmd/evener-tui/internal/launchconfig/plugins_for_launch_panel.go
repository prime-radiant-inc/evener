package launchconfig

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

// PluginsForLaunchResult is the result of the dedicated New Session plugin
// selector. A non-nil empty EnabledPlugins is meaningful: it explicitly
// disables all plugins for this launch.
type PluginsForLaunchResult struct {
	Applied        bool
	Cancelled      bool
	EnabledPlugins *[]string
}

// PluginsForLaunchPanel selects the plugins for one upcoming session. It is
// intentionally separate from PluginsPanel, which manages the global registry.
type PluginsForLaunchPanel struct {
	plugins         []appwire.PluginLaunchCandidate
	selected        map[string]bool
	selectionOrder  []string
	initial         map[string]bool
	initialProvided bool
	selectionErrors map[string]string
	diagnostics     []string
	filter          string
	cursor          int
	done            bool
	cancelled       bool
	applied         bool
	width           int
	previewErr      error
}

func NewPluginsForLaunchPanel(preview appwire.PluginPreviewResponse, initial *[]string, width int) PluginsForLaunchPanel {
	p := PluginsForLaunchPanel{
		plugins:         append([]appwire.PluginLaunchCandidate(nil), preview.Plugins...),
		selected:        map[string]bool{},
		initial:         map[string]bool{},
		selectionErrors: map[string]string{},
		width:           width,
		initialProvided: initial != nil,
	}
	for _, failure := range preview.SelectionErrors {
		p.selectionErrors[failure.Name] = failure.Reason
	}
	for _, diagnostic := range preview.Diagnostics {
		if message := strings.TrimSpace(diagnostic.Message); message != "" {
			p.diagnostics = append(p.diagnostics, message)
		}
	}
	if initial == nil {
		for _, plugin := range preview.Plugins {
			if plugin.Selected {
				p.selected[plugin.Name] = true
				p.selectionOrder = append(p.selectionOrder, plugin.Name)
			}
		}
	} else {
		for _, name := range *initial {
			if name == "" || p.selected[name] {
				continue
			}
			p.selected[name] = true
			p.initial[name] = true
			p.selectionOrder = append(p.selectionOrder, name)
		}
	}
	return p
}

func (p PluginsForLaunchPanel) Init() tea.Cmd { return nil }

func (p PluginsForLaunchPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case PluginPreviewResultMsg:
		if v.Err != nil {
			p.previewErr = v.Err
			return p, nil
		}
		p.previewErr = nil
		p.plugins = append([]appwire.PluginLaunchCandidate(nil), v.Response.Plugins...)
		p.selectionErrors = map[string]string{}
		for _, failure := range v.Response.SelectionErrors {
			p.selectionErrors[failure.Name] = failure.Reason
		}
		p.diagnostics = nil
		for _, diagnostic := range v.Response.Diagnostics {
			if message := strings.TrimSpace(diagnostic.Message); message != "" {
				p.diagnostics = append(p.diagnostics, message)
			}
		}
		if !p.initialProvided {
			p.selected = map[string]bool{}
			p.selectionOrder = nil
			for _, plugin := range p.plugins {
				if plugin.Selected {
					p.selected[plugin.Name] = true
					p.selectionOrder = append(p.selectionOrder, plugin.Name)
				}
			}
		}
		p.cursor = min(p.cursor, max(len(p.filtered())-1, 0))
		return p, nil
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, func() tea.Msg { return PluginsForLaunchResultMsg{Cancelled: true} }
		case tea.KeyEnter:
			if p.previewErr != nil || p.hasBlockingSelectionError() {
				return p, nil
			}
			values := p.selectedValues()
			p.applied = true
			p.done = true
			return p, func() tea.Msg { return PluginsForLaunchResultMsg{Applied: true, EnabledPlugins: values} }
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			if p.cursor < len(p.filtered())-1 {
				p.cursor++
			}
		case tea.KeyBackspace:
			p.filter = trimLastRune(p.filter)
			p.cursor = 0
		case tea.KeySpace:
			p.toggleCursor()
		case tea.KeyRunes:
			for _, r := range v.Runes {
				switch r {
				case 'a':
					p.selectVisible()
				case 'n':
					p.selected = map[string]bool{}
					p.selectionOrder = nil
				default:
					p.filter += string(r)
					p.cursor = 0
				}
			}
		}
	}
	return p, nil
}

// PluginsForLaunchResultMsg is emitted when the selector closes.
type PluginsForLaunchResultMsg = pluginsForLaunchResultMsg

// This private alias keeps the public result message's construction centralized
// while retaining the exact exported message name used by the hub.
type pluginsForLaunchResultMsg = PluginsForLaunchResult

func (p PluginsForLaunchPanel) filtered() []appwire.PluginLaunchCandidate {
	if p.filter == "" {
		return p.plugins
	}
	needle := strings.ToLower(p.filter)
	filtered := make([]appwire.PluginLaunchCandidate, 0, len(p.plugins))
	for _, plugin := range p.plugins {
		if strings.Contains(strings.ToLower(plugin.Name), needle) ||
			strings.Contains(strings.ToLower(plugin.Source), needle) ||
			strings.Contains(strings.ToLower(plugin.Description), needle) {
			filtered = append(filtered, plugin)
		}
	}
	return filtered
}

func (p PluginsForLaunchPanel) selectedValues() *[]string {
	values := make([]string, 0, len(p.selected))
	seen := map[string]bool{}
	for _, name := range p.selectionOrder {
		if p.selected[name] && !seen[name] {
			values = append(values, name)
			seen[name] = true
		}
	}
	var rest []string
	for name := range p.selected {
		if p.selected[name] && !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	values = append(values, rest...)
	return &values
}

func (p *PluginsForLaunchPanel) toggleCursor() {
	filtered := p.filtered()
	if p.cursor < 0 || p.cursor >= len(filtered) {
		return
	}
	name := filtered[p.cursor].Name
	p.selected[name] = !p.selected[name]
	if p.selected[name] {
		p.selectionOrder = append(p.selectionOrder, name)
	}
}

func (p *PluginsForLaunchPanel) selectVisible() {
	for _, plugin := range p.filtered() {
		if !p.selected[plugin.Name] {
			p.selectionOrder = append(p.selectionOrder, plugin.Name)
		}
		p.selected[plugin.Name] = true
	}
}

func (p PluginsForLaunchPanel) hasBlockingSelectionError() bool {
	for name := range p.selected {
		if p.selected[name] && strings.TrimSpace(p.selectionErrors[name]) != "" {
			return true
		}
	}
	return false
}

func (p PluginsForLaunchPanel) Done() bool { return p.done }

func (p PluginsForLaunchPanel) Result() PluginsForLaunchResult {
	return PluginsForLaunchResult{Applied: p.applied, Cancelled: p.cancelled, EnabledPlugins: func() *[]string {
		if !p.applied {
			return nil
		}
		return p.selectedValues()
	}()}
}

func (p PluginsForLaunchPanel) View() string {
	width := p.width
	if width <= 0 {
		width = 80
	}
	width = min(max(width, 30), 80)
	var body strings.Builder
	if p.filter == "" {
		body.WriteString("Filter: (type to filter)\n\n")
	} else {
		body.WriteString("Filter: ")
		body.WriteString(p.filter)
		body.WriteString("\n\n")
	}
	if p.previewErr != nil {
		body.WriteString("Preview failed: ")
		body.WriteString(p.previewErr.Error())
		body.WriteString("\nRetry after returning to the field.\n")
	} else {
		filtered := p.filtered()
		start := 0
		if len(filtered) > 15 {
			start = max(min(p.cursor-7, len(filtered)-15), 0)
		}
		end := min(start+15, len(filtered))
		for i := start; i < end; i++ {
			plugin := filtered[i]
			marker := "[ ]"
			if p.selected[plugin.Name] {
				marker = "[x]"
			}
			prefix := "  "
			if i == p.cursor {
				prefix = "> "
			}
			line := fmt.Sprintf("%s%s %s", prefix, marker, plugin.Name)
			if plugin.Source != "" {
				line += "  " + plugin.Source
			}
			if plugin.Description != "" {
				line += " — " + plugin.Description
			}
			if reason := strings.TrimSpace(p.selectionErrors[plugin.Name]); reason != "" {
				line += " [error: " + reason + "]"
			}
			line = ansi.Truncate(line, max(width-4, 1), "…")
			body.WriteString(lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().Text).Render(line))
			body.WriteByte('\n')
		}
		if len(filtered) == 0 {
			body.WriteString("No matching plugins.\n")
		}
		for _, diagnostic := range p.diagnostics {
			body.WriteString(lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().TextDim).Render("diagnostic: " + diagnostic))
			body.WriteByte('\n')
		}
	}
	footer := tuiprim.ActionBarForWidth(width,
		tuiprim.KbdHint("↑↓", "navigate"), tuiprim.KbdHint("space", "toggle"),
		tuiprim.KbdHint("a", "all visible"), tuiprim.KbdHint("n", "none"),
		tuiprim.KbdHint("enter", "apply"), tuiprim.KbdHint("esc", "cancel"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Plugins for launch", Width: width, Body: body.String(), Footer: footer})
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return s
	}
	return string(runes[:len(runes)-1])
}
