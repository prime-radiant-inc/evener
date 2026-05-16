package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

type launchTab int

const (
	launchTabGlobal launchTab = iota
	launchTabProject
	launchTabRepo
)

type launchSettingsPanel struct {
	client         *appwire.Client
	cwd            string
	tab            launchTab
	global         appwire.LaunchConfigLayer
	project        appwire.LaunchConfigLayer
	resolved       appwire.LaunchConfigResolved
	loadingGlobal  bool
	loadingProj    bool
	loadingResolve bool
	cursor         int
	statusMessage  string
	done           bool
	cancelled      bool
}

func newLaunchSettingsPanel(client *appwire.Client, cwd string) launchSettingsPanel {
	return launchSettingsPanel{client: client, cwd: cwd, loadingGlobal: true, loadingProj: true, loadingResolve: true}
}

func (p launchSettingsPanel) initialCmd() tea.Cmd {
	if p.client == nil {
		return tea.Batch(
			func() tea.Msg { return launchLayerResultMsg{Layer: "global", Err: nil} },
		)
	}
	return tea.Batch(
		cmdGetLayer(p.client, p.cwd, "global"),
		cmdGetLayer(p.client, p.cwd, "project"),
		cmdResolveLaunch(p.client, p.cwd, nil),
	)
}

func (p launchSettingsPanel) Init() tea.Cmd { return nil }

func (p launchSettingsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case launchLayerResultMsg:
		if m.Err != nil {
			p.statusMessage = "load error: " + m.Err.Error()
			return p, nil
		}
		switch m.Layer {
		case "global":
			p.global = m.Data
			p.loadingGlobal = false
		case "project":
			p.project = m.Data
			p.loadingProj = false
		}
	case launchResolveResultMsg:
		p.resolved = m.Resolved
		p.loadingResolve = false
		if m.Err != nil {
			p.statusMessage = "resolve error: " + m.Err.Error()
		}
	case launchSetLayerResultMsg:
		p.statusMessage = "saved " + m.Layer
		p.resolved = m.Resolved
	case launchTrustResultMsg:
		p.resolved = m.Resolved
		p.statusMessage = "trust recorded"
	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, nil
		case tea.KeyLeft:
			if p.tab > 0 {
				p.tab--
				p.cursor = 0
			}
		case tea.KeyRight:
			if p.tab < launchTabRepo {
				p.tab++
				p.cursor = 0
			}
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			p.cursor++
		case tea.KeyEnter:
			return p.editCurrent()
		}
	}
	return p, nil
}

func (p launchSettingsPanel) View() string {
	var b strings.Builder
	tabs := []string{"Global", "Project", "In-Repo"}
	for i, name := range tabs {
		if launchTab(i) == p.tab {
			fmt.Fprintf(&b, "[%s] ", name)
		} else {
			fmt.Fprintf(&b, " %s  ", name)
		}
	}
	b.WriteString("\n\n")
	switch p.tab {
	case launchTabGlobal:
		b.WriteString(renderLayerView("global", p.global, p.cursor))
	case launchTabProject:
		b.WriteString("cwd: " + p.cwd + "\n")
		b.WriteString(renderLayerView("project", p.project, p.cursor))
	case launchTabRepo:
		b.WriteString(renderRepoView(p.resolved.Repo))
	}
	if p.statusMessage != "" {
		fmt.Fprintf(&b, "\n%s", p.statusMessage)
	}
	b.WriteString("\n[←/→] tab  [↑/↓] field  [Enter] edit  [Esc] close")
	return b.String()
}

func renderLayerView(label string, l appwire.LaunchConfigLayer, cursor int) string {
	var b strings.Builder
	rows := layerRows(l)
	for i, r := range rows {
		c := "  "
		if i == cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	return b.String()
}

type layerRow struct {
	field string
	label string
	value string
}

func layerRows(l appwire.LaunchConfigLayer) []layerRow {
	ptrIntStr := func(p *int) string {
		if p == nil {
			return "(default)"
		}
		return fmt.Sprintf("%d", *p)
	}
	ptrBoolStr := func(p *bool) string {
		if p == nil {
			return "(default)"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	return []layerRow{
		{"model", "model", l.Model},
		{"agent", "agent", l.Agent},
		{"reasoning_effort", "reasoning_effort", l.ReasoningEffort},
		{"context_strategy", "context_strategy", l.ContextStrategy},
		{"max_rounds", "max_rounds", ptrIntStr(l.MaxRounds)},
		{"max_subagent_depth", "max_subagent_depth", ptrIntStr(l.MaxSubagentDepth)},
		{"no_project_prompts", "no_project_prompts", ptrBoolStr(l.NoProjectPrompts)},
		{"skills_dirs", "skills_dirs", fmt.Sprintf("%d entries", len(l.SkillsDirs))},
		{"plugin_dirs", "plugin_dirs", fmt.Sprintf("%d entries", len(l.PluginDirs))},
		{"mcp_configs", "mcp_configs", fmt.Sprintf("%d entries", len(l.MCPConfigs))},
		{"mcps", "mcps", fmt.Sprintf("%d entries", len(l.MCPs))},
		{"env", "env", fmt.Sprintf("%d entries", len(l.Env))},
	}
}

func renderRepoView(r *appwire.RepoLaunchConfigStatus) string {
	if r == nil {
		return "no in-repo file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "path:  %s\ntrust: %s\nhash:  %s\n\n", r.Path, r.Trust, r.Hash)
	if r.Preview != "" {
		fmt.Fprintf(&b, "preview:\n%s\n", r.Preview)
	}
	if r.Trust == "untrusted" || r.Trust == "changed" {
		b.WriteString("\n[T] trust this file")
	}
	return b.String()
}

func (p launchSettingsPanel) editCurrent() (tea.Model, tea.Cmd) {
	// Task 6 will fill this in. For now, just note that editing isn't wired.
	p.statusMessage = "(editor not yet wired; coming in Task 6)"
	return p, nil
}
