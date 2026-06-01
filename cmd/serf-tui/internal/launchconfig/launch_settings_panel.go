package launchconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
)

type launchTab int

const (
	launchTabGlobal launchTab = iota
	launchTabProject
	launchTabRepo
)

type LaunchSettingsPanel struct {
	client         *appwire.Client
	cwd            string
	tab            launchTab
	global         appwire.LaunchConfigLayer
	project        appwire.LaunchConfigLayer
	resolved       appwire.LaunchConfigResolved
	schema         []appwire.LaunchOption
	loadingGlobal  bool
	loadingProj    bool
	loadingResolve bool
	cursor         int
	statusMessage  string
	done           bool
	cancelled      bool
}

func NewLaunchSettingsPanel(client *appwire.Client, cwd string) LaunchSettingsPanel {
	return LaunchSettingsPanel{client: client, cwd: cwd, loadingGlobal: true, loadingProj: true, loadingResolve: true}
}

func (p LaunchSettingsPanel) InitialCmd() tea.Cmd {
	if p.client == nil {
		return tea.Batch(
			func() tea.Msg { return LaunchLayerResultMsg{Layer: "global", Err: nil} },
		)
	}
	return tea.Batch(
		CmdLaunchSchema(p.client),
		CmdGetLayer(p.client, p.cwd, "global"),
		CmdGetLayer(p.client, p.cwd, "project"),
		CmdResolveLaunch(p.client, p.cwd, nil),
	)
}

func (p LaunchSettingsPanel) Init() tea.Cmd { return nil }

func (p LaunchSettingsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case LaunchLayerResultMsg:
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
	case LaunchSchemaResultMsg:
		if m.Err == nil {
			p.schema = m.Schema.Options
		}
	case LaunchResolveResultMsg:
		p.resolved = m.Resolved
		p.loadingResolve = false
		if m.Err != nil {
			p.statusMessage = "resolve error: " + m.Err.Error()
		}
	case LaunchSetLayerResultMsg:
		if m.Err != nil {
			p.statusMessage = "save error: " + m.Err.Error()
		} else {
			p.statusMessage = "saved " + m.Layer
			p.resolved = m.Resolved
		}
	case LaunchTrustResultMsg:
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

func (p LaunchSettingsPanel) renderTabs() string {
	var b strings.Builder
	tabs := []string{"Global", "Project", "In-Repo"}
	for i, name := range tabs {
		if launchTab(i) == p.tab {
			fmt.Fprintf(&b, "[%s] ", name)
		} else {
			fmt.Fprintf(&b, " %s  ", name)
		}
	}
	return b.String()
}

func (p LaunchSettingsPanel) renderActiveTab() string {
	var b strings.Builder
	switch p.tab {
	case launchTabGlobal:
		b.WriteString(p.renderLayerView("global", p.global, p.cursor))
	case launchTabProject:
		b.WriteString("cwd: " + p.cwd + "\n")
		b.WriteString(p.renderLayerView("project", p.project, p.cursor))
	case launchTabRepo:
		b.WriteString(renderRepoView(p.resolved.Repo))
	}
	if p.statusMessage != "" {
		fmt.Fprintf(&b, "\n%s", p.statusMessage)
	}
	return b.String()
}

// Done reports whether the panel has been dismissed.
func (p LaunchSettingsPanel) Done() bool { return p.done }

// CWD returns the working directory the panel resolves launch config against.
func (p LaunchSettingsPanel) CWD() string { return p.cwd }

func (p LaunchSettingsPanel) View() string {
	body := p.renderTabs() + "\n\n" + p.renderActiveTab()
	width := 80
	footer := tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("←→", "tab"), tuiprim.KbdHint("↑↓", "field"), tuiprim.KbdHint("enter", "edit"), tuiprim.KbdHint("esc", "close"))
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Launch settings", Width: width, Body: body, Footer: footer})
}

func (p LaunchSettingsPanel) renderLayerView(label string, l appwire.LaunchConfigLayer, cursor int) string {
	var b strings.Builder
	rows := p.rowsForLayer(label, l)
	for i, r := range rows {
		c := "  "
		if i == cursor {
			c = "> "
		}
		fmt.Fprintf(&b, "%s%-22s %s\n", c, r.label, r.value)
	}
	return b.String()
}

func renderLayerView(label string, l appwire.LaunchConfigLayer, cursor int) string {
	p := LaunchSettingsPanel{}
	return p.renderLayerView(label, l, cursor)
}

type layerRow struct {
	field          string
	label          string
	value          string
	editValue      string
	pathCompletion bool
}

func (p LaunchSettingsPanel) rowsForLayer(layerName string, l appwire.LaunchConfigLayer) []layerRow {
	if len(p.schema) > 0 {
		return launchSchemaRows(p.schema, l, layerName, launchSchemaRowsSettings)
	}
	return layerRows(l)
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
		{"model", "model", l.Model, l.Model, false},
		{"fast_cheap_model", "fast_cheap_model", l.FastCheapModel, l.FastCheapModel, false},
		{"agent", "agent", l.Agent, l.Agent, false},
		{"reasoning_effort", "reasoning_effort", l.ReasoningEffort, l.ReasoningEffort, false},
		{"context_strategy", "context_strategy", l.ContextStrategy, l.ContextStrategy, false},
		{"max_rounds", "max_rounds", ptrIntStr(l.MaxRounds), ptrIntStr(l.MaxRounds), false},
		{"max_subagent_depth", "max_subagent_depth", ptrIntStr(l.MaxSubagentDepth), ptrIntStr(l.MaxSubagentDepth), false},
		{"no_project_prompts", "no_project_prompts", ptrBoolStr(l.NoProjectPrompts), ptrBoolStr(l.NoProjectPrompts), false},
		{"skills_dirs", "skills_dirs", fmt.Sprintf("%d entries", len(l.SkillsDirs)), strings.Join(l.SkillsDirs, ", "), true},
		{"plugin_dirs", "plugin_dirs", fmt.Sprintf("%d entries", len(l.PluginDirs)), strings.Join(l.PluginDirs, ", "), true},
		{"mcp_configs", "mcp_configs", fmt.Sprintf("%d entries", len(l.MCPConfigs)), strings.Join(l.MCPConfigs, ", "), true},
		{"mcps", "mcps", fmt.Sprintf("%d entries", len(l.MCPs)), mcpEditValue(l.MCPs), false},
		{"env", "env", fmt.Sprintf("%d entries", len(l.Env)), "", false},
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

// LaunchSettingsEditRequestMsg is emitted when the user presses Enter on
// an editable field; the hub model translates it into a textInputModal.
type LaunchSettingsEditRequestMsg struct {
	Layer          string
	Field          string
	CurrentValue   string
	PathCompletion bool
}

func (p LaunchSettingsPanel) editCurrent() (tea.Model, tea.Cmd) {
	if p.tab == launchTabRepo {
		// In-repo tab: Enter applies trust when state is untrusted/changed.
		if p.resolved.Repo == nil || p.resolved.Repo.Hash == "" {
			return p, nil
		}
		if p.resolved.Repo.Trust == "untrusted" || p.resolved.Repo.Trust == "changed" {
			return p, CmdTrustRepo(p.client, p.cwd, p.resolved.Repo.Hash)
		}
		return p, nil
	}
	rows := p.rowsForLayer(p.tabName(), p.currentLayer())
	if p.cursor >= len(rows) {
		return p, nil
	}
	row := rows[p.cursor]
	if launchSettingsFieldReadOnly(row.field) {
		return p, nil
	}
	return p, func() tea.Msg {
		return LaunchSettingsEditRequestMsg{
			Layer:          p.tabName(),
			Field:          row.field,
			CurrentValue:   row.editValue,
			PathCompletion: row.pathCompletion,
		}
	}
}

func (p LaunchSettingsPanel) tabName() string {
	switch p.tab {
	case launchTabProject:
		return "project"
	default:
		return "global"
	}
}

func (p LaunchSettingsPanel) currentLayer() appwire.LaunchConfigLayer {
	if p.tab == launchTabProject {
		return p.project
	}
	return p.global
}

// ApplyEdit returns a copy of the current panel with the field updated to
// `value`. Used by the hub model after a textInputModal returns a result.
func (p LaunchSettingsPanel) ApplyEdit(field, value string) (LaunchSettingsPanel, appwire.LaunchConfigLayer, error) {
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
	case "app_replay_size":
		v, err := parseOptionalInt(value)
		if err != nil {
			return layer, err
		}
		layer.AppReplaySize = v
	case "no_project_prompts":
		v, err := parseOptionalBool(value)
		if err != nil {
			return layer, err
		}
		layer.NoProjectPrompts = v
	case "verbose":
		v, err := parseOptionalBool(value)
		if err != nil {
			return layer, err
		}
		layer.Verbose = v
	case "system_prompt_mode":
		layer.SystemPromptMode = strings.TrimSpace(value)
	case "system_prompt_file":
		v := strings.TrimSpace(value)
		if v != "" && v != "(default)" {
			if err := validateLocalLaunchPath(v, "file"); err != nil {
				return layer, err
			}
		}
		if v == "(default)" {
			v = ""
		}
		layer.SystemPromptFile = v
	case "system_prompt_text":
		if strings.TrimSpace(value) == "(default)" {
			layer.SystemPromptText = ""
		} else {
			layer.SystemPromptText = value
		}
	case "system_prompt_append_mode":
		layer.SystemPromptAppendMode = strings.TrimSpace(value)
	case "system_prompt_append_file":
		v := strings.TrimSpace(value)
		if v != "" && v != "(default)" {
			if err := validateLocalLaunchPath(v, "file"); err != nil {
				return layer, err
			}
		}
		if v == "(default)" {
			v = ""
		}
		layer.SystemPromptAppendFile = v
	case "system_prompt_append_text":
		if strings.TrimSpace(value) == "(default)" {
			layer.SystemPromptAppendText = ""
		} else {
			layer.SystemPromptAppendText = value
		}
	case "trace_file", "cpu_profile", "export_atif_path":
		v := strings.TrimSpace(value)
		if v != "" && v != "(default)" {
			if err := validateLocalLaunchPath(v, "outputFile"); err != nil {
				return layer, err
			}
		}
		if v == "(default)" {
			v = ""
		}
		switch field {
		case "trace_file":
			layer.TraceFile = v
		case "cpu_profile":
			layer.CPUProfile = v
		case "export_atif_path":
			layer.ExportATIFPath = v
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
	case "mcps":
		mcps, err := parseMCPs(value)
		if err != nil {
			return layer, err
		}
		layer.MCPs = mcps
	case "model_fallbacks":
		layer.ModelFallbacks = parseModelFallbacks(value)
	case "env":
		env, err := parseEnvMap(value)
		if err != nil {
			return layer, err
		}
		layer.Env = env
	default:
		return layer, fmt.Errorf("editing %q in TUI not yet supported; use the web UI", field)
	}
	return layer, nil
}

func parseModelFallbacks(value string) []string {
	switch strings.TrimSpace(value) {
	case "", "(default)":
		return nil
	case "[]":
		return []string{}
	default:
		return splitTrim(value, ",")
	}
}

func parseOptionalBool(value string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "(default)":
		return nil, nil
	case "true", "yes", "1":
		t := true
		return &t, nil
	case "false", "no", "0":
		f := false
		return &f, nil
	default:
		return nil, fmt.Errorf("bool required, got %q", value)
	}
}

func LaunchSettingsFieldUsesPathCompletion(field string) bool {
	switch field {
	case "skills_dirs", "plugin_dirs", "mcp_configs", "system_prompt_file", "system_prompt_append_file", "trace_file", "cpu_profile", "export_atif_path":
		return true
	default:
		return false
	}
}

func launchSettingsFieldReadOnly(field string) bool {
	return false
}

func mcpEditValue(mcps []appwire.MCPServerSpec) string {
	if len(mcps) == 0 {
		return ""
	}
	specs := make([]mcpEditSpec, 0, len(mcps))
	for _, mcp := range mcps {
		specs = append(specs, mcpEditSpec{Name: mcp.Name, Command: mcp.Command, Args: mcp.Args})
	}
	data, err := json.Marshal(specs)
	if err != nil {
		return ""
	}
	return string(data)
}

type mcpEditSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func parseMCPs(value string) ([]appwire.MCPServerSpec, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "[") {
		var mcps []appwire.MCPServerSpec
		if err := json.Unmarshal([]byte(value), &mcps); err != nil {
			return nil, fmt.Errorf("mcps JSON: %w", err)
		}
		if err := validateMCPs(mcps); err != nil {
			return nil, err
		}
		return mcps, nil
	}
	if strings.HasPrefix(value, "{") {
		var mcp appwire.MCPServerSpec
		if err := json.Unmarshal([]byte(value), &mcp); err != nil {
			return nil, fmt.Errorf("mcp JSON: %w", err)
		}
		mcps := []appwire.MCPServerSpec{mcp}
		if err := validateMCPs(mcps); err != nil {
			return nil, err
		}
		return mcps, nil
	}
	rows := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ';'
	})
	out := make([]appwire.MCPServerSpec, 0, len(rows))
	for i, line := range rows {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		rest = strings.TrimSpace(rest)
		if !ok || name == "" {
			return nil, fmt.Errorf("mcp line %d must be name:command args...", i+1)
		}
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return nil, fmt.Errorf("mcp line %d missing command", i+1)
		}
		command := parts[0]
		if err := validateLocalLaunchPath(command, "command"); err != nil {
			return nil, fmt.Errorf("mcp line %d command %q: %w", i+1, command, err)
		}
		out = append(out, appwire.MCPServerSpec{
			Name:    name,
			Command: command,
			Args:    append([]string(nil), parts[1:]...),
		})
	}
	return out, nil
}

func validateMCPs(mcps []appwire.MCPServerSpec) error {
	for i, mcp := range mcps {
		name := strings.TrimSpace(mcp.Name)
		command := strings.TrimSpace(mcp.Command)
		if name == "" {
			return fmt.Errorf("mcp entry %d missing name", i+1)
		}
		if command == "" {
			return fmt.Errorf("mcp entry %d missing command", i+1)
		}
		if err := validateLocalLaunchPath(command, "command"); err != nil {
			return fmt.Errorf("mcp entry %d command %q: %w", i+1, command, err)
		}
	}
	return nil
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
	if kind == "outputFile" {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return fmt.Errorf("path is a directory")
		}
		parent := filepath.Dir(path)
		info, err := os.Stat(parent)
		if err != nil {
			return fmt.Errorf("parent directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("parent path is not a directory")
		}
		if info.Mode().Perm()&0o222 == 0 {
			return fmt.Errorf("parent directory is not writable")
		}
		return nil
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

func parseEnvMap(value string) (map[string]string, error) {
	entries := splitTrim(value, ",")
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, val, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("env entries must be KEY=value")
		}
		out[key] = strings.TrimSpace(val)
	}
	return out, nil
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
