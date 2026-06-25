// Package plugin loads Claude Code-style plugins from disk — their manifest,
// skills, subagents, hooks, and MCP server configs — into typed values the
// agent engine consumes. It also parses per-project plugin settings.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/skill"
)

// Manifest represents a parsed plugin.json file.
// Fields like Author, Commands, Agents, Hooks, and MCPServers use
// json.RawMessage because their shapes vary (string, array, or object).
type Manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Author      json.RawMessage `json:"author,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	Repository  string          `json:"repository,omitempty"`
	License     string          `json:"license,omitempty"`
	Keywords    []string        `json:"keywords,omitempty"`
	Commands    json.RawMessage `json:"commands,omitempty"`
	Agents      json.RawMessage `json:"agents,omitempty"`
	Hooks       json.RawMessage `json:"hooks,omitempty"`
	MCPServers  json.RawMessage `json:"mcpServers,omitempty"`
}

// kebabCaseRe matches valid kebab-case names: lowercase alphanumeric,
// optionally separated by single hyphens, no leading/trailing hyphens.
var kebabCaseRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validatePluginName checks that name is non-empty kebab-case
// (lowercase alphanumeric with hyphens, no leading/trailing hyphens).
func validatePluginName(name string) error {
	if name == "" {
		return errors.New("plugin name must not be empty")
	}
	if !kebabCaseRe.MatchString(name) {
		return fmt.Errorf("plugin name %q must be kebab-case (lowercase alphanumeric with hyphens, no leading/trailing hyphens)", name)
	}
	return nil
}

// ParseManifest unmarshals JSON plugin manifest data and validates
// the required name field.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parsing plugin manifest: %w", err)
	}
	if err := validatePluginName(m.Name); err != nil {
		return Manifest{}, fmt.Errorf("invalid plugin manifest: %w", err)
	}
	return m, nil
}

// expandPluginRoot replaces plugin-root placeholders with pluginDir in s.
func expandPluginRoot(s string, pluginDir string) string {
	s = strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
	return strings.ReplaceAll(s, "${PLUGIN_ROOT}", pluginDir)
}

// Instance represents a plugin that has been loaded from disk.
type Instance struct {
	Manifest Manifest // parsed plugin.json
	Dir      string   // absolute path = CLAUDE_PLUGIN_ROOT
	// ManifestFlavor is which manifest directory the plugin was loaded from:
	// "claude" for .claude-plugin, "codex" for .codex-plugin. A plugin that
	// ships both flavors loads the Claude one (see Load); the flavor is surfaced
	// at load time so a wrong-flavor load (e.g. codex hooks that re-inject on
	// resume) is diagnosable.
	ManifestFlavor string
	// ManifestPath is the absolute path of the plugin.json that was loaded.
	ManifestPath string
	Skills       map[string]skill.SkillMeta     // namespaced as "plugin-name:skill-name"
	Agents       map[string]Agent               // namespaced as "plugin-name:agent-name"
	Hooks        map[HookEvent][]RegisteredHook // keyed by event type
	MCPConfigs   []mcpconfig.ServerConfig       // namespaced as "plugin_<name>_<server>"

	// UnsupportedHooks is the set of Claude-recognized events declared by this
	// plugin that serf does not currently fire (tier: reserved-placeholder).
	// Populated by Load; empty when no such events are declared.
	UnsupportedHooks map[HookEvent]bool
	// UnknownHooks is the set of event names declared by this plugin that serf
	// does not recognize as Claude events at all.
	UnknownHooks map[string]bool
}

// discoverPluginSkills scans a plugin's skills directories and returns
// skills namespaced as "pluginName:skillName".
func discoverPluginSkills(pluginDir, pluginName string) map[string]skill.SkillMeta {
	dirs := resolveComponentDirs(pluginDir, "skills", nil)
	raw := map[string]skill.SkillMeta{}
	for _, dir := range dirs {
		skill.ScanSkillsDir(dir, raw)
	}
	namespaced := make(map[string]skill.SkillMeta, len(raw))
	for name, meta := range raw {
		namespaced[pluginName+":"+name] = meta
	}
	return namespaced
}

// discoverPluginMCPConfigs reads MCP server configs from a plugin's .mcp.json
// file and/or inline manifest mcpServers field. Server names are prefixed with
// "plugin_<pluginName>_" and ${CLAUDE_PLUGIN_ROOT} is expanded to pluginDir.
func discoverPluginMCPConfigs(pluginDir string, manifestMCPServers json.RawMessage, pluginName string) ([]mcpconfig.ServerConfig, error) {
	var layers [][]mcpconfig.ServerConfig

	// Layer 1: .mcp.json file in the plugin directory.
	mcpPath := filepath.Join(pluginDir, ".mcp.json")
	if fileConfigs, err := loadPluginMCPFile(mcpPath, pluginDir); err == nil {
		layers = append(layers, fileConfigs)
	}
	// Missing file is not an error.

	// Layer 2: Inline mcpServers from the manifest.
	if len(manifestMCPServers) > 0 {
		expanded := expandPluginRoot(string(manifestMCPServers), pluginDir)
		var servers map[string]json.RawMessage
		if err := json.Unmarshal([]byte(expanded), &servers); err == nil && len(servers) > 0 {
			inlineConfigs, err := mcpconfig.ParseServerMap(servers, "inline")
			if err != nil {
				return nil, err
			}
			layers = append(layers, inlineConfigs)
		}
	}

	merged := mcpconfig.Merge(layers...)
	if len(merged) == 0 {
		return nil, nil
	}

	// Namespace server names.
	prefix := "plugin_" + pluginName + "_"
	for i := range merged {
		merged[i].Name = prefix + merged[i].Name
	}
	return merged, nil
}

// loadPluginMCPFile reads a plugin's .mcp.json file, expands
// ${CLAUDE_PLUGIN_ROOT} in the raw JSON, then parses server configs.
func loadPluginMCPFile(path, pluginDir string) ([]mcpconfig.ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand ${CLAUDE_PLUGIN_ROOT} before env-var expansion so
	// expandEnvVars (called by serverJSONToConfig) doesn't fail on it.
	expanded := expandPluginRoot(string(data), pluginDir)

	var cf struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(expanded), &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	return mcpconfig.ParseServerMap(cf.MCPServers, path)
}

// Load reads a plugin manifest from <dir>/.claude-plugin/plugin.json, falling
// back to <dir>/.codex-plugin/plugin.json only when no Claude manifest exists,
// parses it, and returns an Instance with Dir set to the resolved absolute path.
// Claude is preferred because serf preserves conversation context across resume
// (it replays the transcript), matching Claude Code's resume semantics; the
// codex flavor's SessionStart hooks re-inject on resume, which double-injects
// under serf. The chosen flavor is recorded in Instance.ManifestFlavor.
func Load(dir string) (Instance, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Instance{}, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return Instance{}, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}

	manifestPath := filepath.Join(resolved, ".claude-plugin", "plugin.json")
	flavor := "claude"
	if _, err := os.Stat(manifestPath); err != nil {
		if !os.IsNotExist(err) {
			return Instance{}, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
		}
		manifestPath = filepath.Join(resolved, ".codex-plugin", "plugin.json")
		flavor = "codex"
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Instance{}, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
	}

	manifest, err := ParseManifest(data)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}

	lp := Instance{Manifest: manifest, Dir: resolved, ManifestFlavor: flavor, ManifestPath: manifestPath}
	lp.Skills = discoverPluginSkills(resolved, manifest.Name)

	agents, err := discoverPluginAgents(resolved, manifest.Agents, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Agents = agents

	hooks, unsupportedHooks, unknownHooks, err := discoverPluginHooksDiag(resolved, manifest.Hooks, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Hooks = hooks
	lp.UnsupportedHooks = unsupportedHooks
	lp.UnknownHooks = unknownHooks

	mcpConfigs, err := discoverPluginMCPConfigs(resolved, manifest.MCPServers, manifest.Name)
	if err != nil {
		return Instance{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.MCPConfigs = mcpConfigs

	return lp, nil
}

// LoadAll loads plugins from multiple directories and checks for
// duplicate plugin names.
func LoadAll(dirs []string) ([]Instance, error) {
	plugins := make([]Instance, 0, len(dirs))
	seen := make(map[string]string) // name -> dir

	for _, dir := range dirs {
		lp, err := Load(dir)
		if err != nil {
			return nil, err
		}
		if prevDir, ok := seen[lp.Manifest.Name]; ok {
			return nil, fmt.Errorf("duplicate plugin name %q: found in %q and %q",
				lp.Manifest.Name, prevDir, lp.Dir)
		}
		seen[lp.Manifest.Name] = lp.Dir
		plugins = append(plugins, lp)
	}
	return plugins, nil
}

// resolveComponentDirs returns a list of absolute directory paths to scan for
// a component type. It always includes <pluginDir>/<defaultName>/ if that
// directory exists on disk. If override is a string, it is resolved relative to
// pluginDir and added. If override is a []any of strings, each is resolved and
// added. Custom paths supplement defaults, they don't replace them.
func resolveComponentDirs(pluginDir string, defaultName string, override any) []string {
	var dirs []string

	// Include the default dir if it exists on disk.
	defaultDir := filepath.Join(pluginDir, defaultName)
	if info, err := os.Stat(defaultDir); err == nil && info.IsDir() {
		dirs = append(dirs, defaultDir)
	}

	// Resolve custom override paths relative to pluginDir.
	switch v := override.(type) {
	case string:
		dirs = append(dirs, filepath.Join(pluginDir, v))
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				dirs = append(dirs, filepath.Join(pluginDir, s))
			}
		}
	}

	return dirs
}
