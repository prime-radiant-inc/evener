package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		if m.Err != nil {
			p.statusMessage = "save error: " + m.Err.Error()
		} else {
			p.statusMessage = "saved " + m.Layer
			p.resolved = m.Resolved
		}
	case launchTrustResultMsg:
		if m.Err != nil {
			p.statusMessage = "trust error: " + m.Err.Error()
		} else {
			p.resolved = m.Resolved
			p.statusMessage = "trust recorded"
		}
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
	field     string
	label     string
	value     string
	editValue string
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
		{"model", "model", l.Model, l.Model},
		{"fast_cheap_model", "fast_cheap_model", l.FastCheapModel, l.FastCheapModel},
		{"agent", "agent", l.Agent, l.Agent},
		{"reasoning_effort", "reasoning_effort", l.ReasoningEffort, l.ReasoningEffort},
		{"context_strategy", "context_strategy", l.ContextStrategy, l.ContextStrategy},
		{"max_rounds", "max_rounds", ptrIntStr(l.MaxRounds), ptrIntStr(l.MaxRounds)},
		{"max_subagent_depth", "max_subagent_depth", ptrIntStr(l.MaxSubagentDepth), ptrIntStr(l.MaxSubagentDepth)},
		{"no_project_prompts", "no_project_prompts", ptrBoolStr(l.NoProjectPrompts), ptrBoolStr(l.NoProjectPrompts)},
		{"skills_dirs", "skills_dirs", fmt.Sprintf("%d entries", len(l.SkillsDirs)), strings.Join(l.SkillsDirs, ", ")},
		{"plugin_dirs", "plugin_dirs", fmt.Sprintf("%d entries", len(l.PluginDirs)), strings.Join(l.PluginDirs, ", ")},
		{"mcp_configs", "mcp_configs", fmt.Sprintf("%d entries", len(l.MCPConfigs)), strings.Join(l.MCPConfigs, ", ")},
		{"mcps", "mcps", fmt.Sprintf("%d entries", len(l.MCPs)), ""},
		{"env", "env", fmt.Sprintf("%d entries", len(l.Env)), ""},
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

// launchSettingsEditRequestMsg is emitted when the user presses Enter on
// an editable field; the hub model translates it into a textInputModal.
type launchSettingsEditRequestMsg struct {
	Layer        string
	Field        string
	CurrentValue string
}

func (p launchSettingsPanel) editCurrent() (tea.Model, tea.Cmd) {
	if p.tab == launchTabRepo {
		// In-repo tab: Enter applies trust when state is untrusted/changed.
		if p.resolved.Repo == nil || p.resolved.Repo.Hash == "" {
			return p, nil
		}
		if p.resolved.Repo.Trust == "untrusted" || p.resolved.Repo.Trust == "changed" {
			return p, cmdTrustRepo(p.client, p.cwd, p.resolved.Repo.Hash)
		}
		return p, nil
	}
	rows := layerRows(p.currentLayer())
	if p.cursor >= len(rows) {
		return p, nil
	}
	row := rows[p.cursor]
	return p, func() tea.Msg {
		return launchSettingsEditRequestMsg{
			Layer:        p.tabName(),
			Field:        row.field,
			CurrentValue: row.editValue,
		}
	}
}

func (p launchSettingsPanel) tabName() string {
	switch p.tab {
	case launchTabProject:
		return "project"
	default:
		return "global"
	}
}

func (p launchSettingsPanel) currentLayer() appwire.LaunchConfigLayer {
	if p.tab == launchTabProject {
		return p.project
	}
	return p.global
}

// ApplyEdit returns a copy of the current panel with the field updated to
// `value`. Used by the hub model after a textInputModal returns a result.
func (p launchSettingsPanel) ApplyEdit(field, value string) (launchSettingsPanel, appwire.LaunchConfigLayer, error) {
	layer := p.currentLayer()
	updated, err := applyEdit(layer, field, value)
	if err != nil {
		return p, layer, err
	}
	if p.tab == launchTabProject {
		p.project = updated
	} else {
		p.global = updated
	}
	return p, updated, nil
}

func applyEdit(layer appwire.LaunchConfigLayer, field, value string) (appwire.LaunchConfigLayer, error) {
	switch field {
	case "model":
		layer.Model = strings.TrimSpace(value)
	case "fast_cheap_model":
		layer.FastCheapModel = strings.TrimSpace(value)
	case "agent":
		layer.Agent = strings.TrimSpace(value)
	case "reasoning_effort":
		layer.ReasoningEffort = strings.TrimSpace(value)
	case "context_strategy":
		layer.ContextStrategy = strings.TrimSpace(value)
	case "max_rounds":
		v, err := parseOptionalInt(value)
		if err != nil {
			return layer, err
		}
		layer.MaxRounds = v
	case "max_subagent_depth":
		v, err := parseOptionalInt(value)
		if err != nil {
			return layer, err
		}
		layer.MaxSubagentDepth = v
	case "no_project_prompts":
		switch strings.TrimSpace(value) {
		case "", "(default)":
			layer.NoProjectPrompts = nil
		case "true", "yes", "1":
			t := true
			layer.NoProjectPrompts = &t
		case "false", "no", "0":
			f := false
			layer.NoProjectPrompts = &f
		default:
			return layer, fmt.Errorf("bool required, got %q", value)
		}
	case "skills_dirs", "plugin_dirs", "mcp_configs", "system_prompt_append":
		entries := splitTrim(value, ",")
		switch field {
		case "skills_dirs":
			if err := validatePathEntries(entries, "dir"); err != nil {
				return layer, err
			}
			layer.SkillsDirs = entries
		case "plugin_dirs":
			if err := validatePathEntries(entries, "dir"); err != nil {
				return layer, err
			}
			layer.PluginDirs = entries
		case "mcp_configs":
			if err := validatePathEntries(entries, "file"); err != nil {
				return layer, err
			}
			layer.MCPConfigs = entries
		case "system_prompt_append":
			layer.SystemPromptAppend = entries
		}
	default:
		return layer, fmt.Errorf("editing %q in TUI not yet supported; use the web UI", field)
	}
	return layer, nil
}

func launchSettingsFieldUsesPathCompletion(field string) bool {
	switch field {
	case "skills_dirs", "plugin_dirs", "mcp_configs":
		return true
	default:
		return false
	}
}

func validatePathEntries(entries []string, kind string) error {
	for _, entry := range entries {
		if err := validateLocalLaunchPath(entry, kind); err != nil {
			return fmt.Errorf("%s: %w", entry, err)
		}
	}
	return nil
}

func validateLocalLaunchPath(path, kind string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(path, "~"))
	}
	if kind == "command" && !strings.ContainsRune(path, filepath.Separator) {
		_, err := exec.LookPath(path)
		return err
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute path required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	switch kind {
	case "dir":
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory")
		}
	case "file":
		if info.IsDir() {
			return fmt.Errorf("path is a directory")
		}
	case "command", "executable":
		if info.IsDir() {
			return fmt.Errorf("path is a directory")
		}
		if info.Mode()&0o111 == 0 {
			return fmt.Errorf("path is not executable")
		}
	}
	return nil
}

func parseOptionalInt(value string) (*int, error) {
	v := strings.TrimSpace(value)
	if v == "" || v == "(default)" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func splitTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
