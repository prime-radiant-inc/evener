package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf-hub/internal/editorurl"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubedge"
	"primeradiant.com/serf/cmd/serf-hub/internal/mcpstatus"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/frontmatter"
)

func (s *WebServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		http.NotFound(w, r)
		return
	}
	section := strings.TrimPrefix(r.URL.Path, "/settings")
	section = strings.TrimPrefix(section, "/")
	if section == "" {
		section = "general"
	}
	// Redirect the legacy /settings/launch URL to the serf-specific tab.
	if section == "launch" {
		http.Redirect(w, r, "/settings/launch-serf", http.StatusFound)
		return
	}
	partialURL := "/_partials/settings/" + section
	// For the per-project settings page, forward the cwd query param.
	if section == "project" {
		if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
			partialURL += "?cwd=" + url.QueryEscape(cwd)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": partialURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) renderSettingsPartial(w http.ResponseWriter, r *http.Request, section string) {
	// Redirect the legacy "launch" section to the serf-specific tab.
	if section == "launch" {
		http.Redirect(w, r, "/_partials/settings/launch-serf", http.StatusFound)
		return
	}
	// Project settings is its own workspace page, not nested in the global
	// settings shell.
	if section == "project" {
		s.renderProjectSettingsPartial(w, r)
		return
	}
	settingsTmpl, ok := s.settingsTmpls[section]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Group launch harness models by provider for the providers page.
	launchModelList, launchModelErr := serfLaunchModelList(r.Context(), s.cfg, "")
	if launchModelErr != nil {
		launchModelList = appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{launchModelListErrorDiagnostic(launchModelErr)},
		}
	}
	var providers []providerDisplay
	byProvider := map[string]int{} // provider name -> index in providers
	for _, m := range launchModelList.Data {
		if idx, exists := byProvider[m.Provider]; exists {
			providers[idx].Models = append(providers[idx].Models, m.Model)
		} else {
			byProvider[m.Provider] = len(providers)
			providers = append(providers, providerDisplay{Name: m.Provider, Models: []string{m.Model}})
		}
	}

	// Built-in agents are compiled into the binary (defaultPersona.txt etc.)
	// and don't have an on-disk file to open. EditPath stays empty so the
	// template can omit the link rather than rendering a broken one.
	agentNames := []string{"default", "explorer", "subagent"}
	agents := make([]agentDisplay, 0, len(agentNames))
	for _, name := range agentNames {
		agents = append(agents, agentDisplay{Name: name})
	}

	var pastCount int
	if s.cfg.Past != nil {
		pastCount = len(s.cfg.Past.AllMetas())
	}

	plugins, pluginsErr := s.discoverPluginsForSettings()
	skills := skillsFromPlugins(plugins)
	mcpPath := s.mcpConfigPathForSettings()
	mcps, mcpsErr := s.discoverMCPsForSettings(mcpPath)

	// Resolve canonical project cwd for the per-project settings page.
	var projectCWD string
	if section == "project" {
		if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
			if abs, err := filepath.Abs(cwd); err == nil {
				projectCWD = abs
			} else {
				projectCWD = cwd
			}
		}
	}

	// Build a deduplicated list of known projects for the project picker.
	var availableProjects []projectListItem
	if section == "project" && projectCWD == "" && s.cfg.Past != nil {
		seen := map[string]bool{}
		for _, meta := range s.cfg.Past.AllMetas() {
			cwd := meta.EnvInfo.WorkingDir
			if cwd == "" || seen[cwd] {
				continue
			}
			seen[cwd] = true
			availableProjects = append(availableProjects, projectListItem{
				CWD:  cwd,
				Name: filepath.Base(cwd),
			})
		}
		sort.Slice(availableProjects, func(i, j int) bool {
			return availableProjects[i].Name < availableProjects[j].Name
		})
	}

	// Compute display-only fields for the general/storage settings pages.
	pastIndexPath := tildeHome(s.cfg.PastIndexPath)
	pastIndexSize := fileSizeHuman(s.cfg.PastIndexPath)
	bearerTokenAge := ""
	if s.cfg.HubStateRoot != "" {
		bearerTokenAge = fileAgeHuman(filepath.Join(s.cfg.HubStateRoot, hubedge.TokenFileName))
	}

	data := settingsData{
		Active:            section,
		HubAddr:           s.cfg.HubAddr,
		RunDir:            s.cfg.RunDir,
		StateDir:          s.cfg.StateDir,
		SpawnTimeout:      "30s",
		PastPerPage:       s.cfg.PastPerPage,
		PastIndexPath:     pastIndexPath,
		PastIndexSize:     pastIndexSize,
		BearerTokenAge:    bearerTokenAge,
		HubVersion:        Version,
		HubCommit:         buildinfo.GitSHA,
		Providers:         providers,
		ModelDiagnostics:  launchModelList.Diagnostics,
		Agents:            agents,
		Plugins:           plugins,
		PluginsError:      errString(pluginsErr),
		Skills:            skills,
		Mcps:              mcps,
		McpsError:         errString(mcpsErr),
		McpConfigPath:     mcpPath,
		PastCount:         pastCount,
		CodexLaunches:     s.cfg.CodexLaunches,
		ProjectCWD:        projectCWD,
		AvailableProjects: availableProjects,
	}

	// Render just the inner settings-content partial when htmx is targeting
	// the inner pane (rail click). Otherwise render the full shell so both
	// rail and content are visible (initial navigation into settings).
	tmplName := "settings"
	if r.Header.Get("HX-Target") == "settings-content" {
		tmplName = "settings-content"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := settingsTmpl.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderProjectSettingsPartial serves the per-project settings page as its
// own workspace partial. It is not wrapped in the global settings shell —
// project settings get their own header (with the project's cwd) and pane.
func (s *WebServer) renderProjectSettingsPartial(w http.ResponseWriter, r *http.Request) {
	var projectCWD string
	if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			projectCWD = abs
		} else {
			projectCWD = cwd
		}
	}

	var availableProjects []projectListItem
	if projectCWD == "" && s.cfg.Past != nil {
		seen := map[string]bool{}
		for _, meta := range s.cfg.Past.AllMetas() {
			cwd := meta.EnvInfo.WorkingDir
			if cwd == "" || seen[cwd] {
				continue
			}
			seen[cwd] = true
			availableProjects = append(availableProjects, projectListItem{
				CWD:  cwd,
				Name: filepath.Base(cwd),
			})
		}
		sort.Slice(availableProjects, func(i, j int) bool {
			return availableProjects[i].Name < availableProjects[j].Name
		})
	}

	data := settingsData{
		Active:            "project",
		ProjectCWD:        projectCWD,
		AvailableProjects: availableProjects,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.projectSettingsTmpl.ExecuteTemplate(w, "project_settings", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// errString returns err.Error() or "" when err is nil. Used to thread
// recoverable settings-discovery errors into the template without a 5xx.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tildeHome replaces the user's home directory prefix in path with "~".
// Returns path unchanged if home is empty or path does not start with home.
func tildeHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	if path == home {
		return "~"
	}
	return path
}

// fileAgeHuman returns a short human-readable description of how long ago the
// file at path was last modified (e.g. "created 3d ago"). Returns "" on error.
func fileAgeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	d := time.Since(info.ModTime())
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("created %dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("created %dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("created %dd ago", int(d.Hours()/24))
	}
}

// fileSizeHuman returns a short human-readable file size string for path
// (e.g. "48 MB"). Returns "" if the file does not exist or stat fails.
func fileSizeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sz := info.Size()
	switch {
	case sz < 1<<10:
		return fmt.Sprintf("%d B", sz)
	case sz < 1<<20:
		return fmt.Sprintf("%d KB", sz>>10)
	case sz < 1<<30:
		return fmt.Sprintf("%d MB", sz>>20)
	default:
		return fmt.Sprintf("%d GB", sz>>30)
	}
}

// defaultPluginsRoot is the conventional XDG location for serf plugins:
// ~/.config/serf/plugins (or $XDG_CONFIG_HOME/serf/plugins).
func defaultPluginsRoot() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins")
}

// defaultMCPConfigPath is the conventional XDG location for the global
// MCP config (~/.config/serf/mcp.json), matching agent.globalMCPConfigPath.
func defaultMCPConfigPath() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "mcp.json")
}

// pluginsRootForSettings returns explicit PluginDirs verbatim, or expands
// the default plugins root into one entry per immediate subdirectory
// containing a .claude-plugin/plugin.json manifest.
func (s *WebServer) pluginsRootForSettings() []string {
	return pluginDirsFromConfig(s.cfg)
}

// pluginDirsFromConfig is pluginsRootForSettings's config-only logic, factored
// out so both the Settings → Plugins pane (via the WebServer method above) and
// the serf/command/list RPC handler (app_rpc.go, which only has the config,
// not a *WebServer) resolve plugin dirs identically.
func pluginDirsFromConfig(cfg hubcore.WebConfig) []string {
	if len(cfg.PluginDirs) > 0 {
		return cfg.PluginDirs
	}
	root := defaultPluginsRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// discoverPluginsForSettings loads plugin manifests for the Settings →
// Plugins pane. Loading is fail-soft (plugin.LoadAllFailSoft), the same way
// hubCommandList and session init load plugins: one broken or mid-edit
// plugin dir must not blank out the whole pane, only its own row. Skip
// reasons aren't surfaced here — the return type has no warning channel —
// but a skipped dir still shows up in a session's own SESSION_START
// warnings. The error return is kept for the caller's existing signature;
// it is now always nil.
func (s *WebServer) discoverPluginsForSettings() ([]pluginDisplay, error) {
	dirs := s.pluginsRootForSettings()
	if len(dirs) == 0 {
		return nil, nil
	}
	loaded, _ := plugin.LoadAllFailSoft(dirs)
	out := make([]pluginDisplay, 0, len(loaded))
	for _, lp := range loaded {
		out = append(out, pluginDisplay{
			Name:    lp.Manifest.Name,
			Path:    lp.Dir,
			Version: lp.Manifest.Version,
			Counts: pluginCounts{
				Skills: len(lp.Skills),
				Agents: len(lp.Agents),
				Mcps:   len(lp.MCPConfigs),
				Hooks:  countHooks(lp.Hooks),
			},
			EditPath: editorurl.EditorURL(filepath.Join(lp.Dir, ".claude-plugin", "plugin.json")),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// countHooks sums all RegisteredHook entries across hook events.
func countHooks(h map[plugin.HookEvent][]plugin.RegisteredHook) int {
	n := 0
	for _, hs := range h {
		n += len(hs)
	}
	return n
}

// skillsFromPlugins flattens per-plugin skills directories into rows for
// the Skills pane. Plugins is the already-loaded list so we know each
// plugin's path and name.
func skillsFromPlugins(plugins []pluginDisplay) []skillDisplay {
	var out []skillDisplay
	for _, p := range plugins {
		out = append(out, collectSkillsForPlugin(p)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// collectSkillsForPlugin scans <pluginPath>/skills/ for SKILL.md files and
// returns one display row per skill. Returns nil if the dir is missing.
func collectSkillsForPlugin(p pluginDisplay) []skillDisplay {
	skillsDir := filepath.Join(p.Path, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	var out []skillDisplay
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		name, desc, ok := readSkillFrontmatter(skillFile)
		if !ok {
			continue
		}
		out = append(out, skillDisplay{
			Name:        name,
			Plugin:      p.Name,
			Description: desc,
			EditPath:    editorurl.EditorURL(skillFile),
		})
	}
	return out
}

// readSkillFrontmatter reads a SKILL.md file and returns its name and
// description from the YAML frontmatter. Returns ("","",false) if the
// file is missing, unparseable, or lacks required fields.
func readSkillFrontmatter(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	doc, err := frontmatter.Parse(string(data))
	if err != nil || doc.Meta == nil {
		return "", "", false
	}
	name, _ := doc.Meta["name"].(string)
	desc, _ := doc.Meta["description"].(string)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(desc) == "" {
		return "", "", false
	}
	return name, desc, true
}

// mcpConfigPathForSettings returns the configured MCP file path, or the
// XDG default when hubcore.WebConfig.MCPConfigPath is empty.
func (s *WebServer) mcpConfigPathForSettings() string {
	if s.cfg.MCPConfigPath != "" {
		return s.cfg.MCPConfigPath
	}
	return defaultMCPConfigPath()
}

// discoverMCPsForSettings reads the MCP config file at path and returns
// rows for the MCP servers pane. A missing file is the empty state, not
// an error. Parse errors return an inline error string.
func (s *WebServer) discoverMCPsForSettings(path string) ([]mcpDisplay, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil //nolint:nilerr // a missing MCP config file is the empty state, not an error
	}
	configs, err := mcpconfig.LoadFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]mcpDisplay, 0, len(configs))
	for _, c := range configs {
		cmd := c.Command
		if cmd == "" {
			cmd = c.URL
		}
		out = append(out, mcpDisplay{
			Name:     c.Name,
			Command:  cmd,
			Args:     c.Args,
			Status:   mcpstatus.ProbeMCPStatus(c),
			Tools:    0,
			Agents:   nil,
			EditPath: editorurl.EditorURL(path),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
