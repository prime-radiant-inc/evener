package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginManifest represents a parsed plugin.json file.
// Fields like Author, Commands, Agents, Hooks, and MCPServers use
// json.RawMessage because their shapes vary (string, array, or object).
type PluginManifest struct {
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
	// serf:naming-ignore — mirrors Claude .mcp.json upstream format
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
}

// kebabCaseRe matches valid kebab-case names: lowercase alphanumeric,
// optionally separated by single hyphens, no leading/trailing hyphens.
var kebabCaseRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validatePluginName checks that name is non-empty kebab-case
// (lowercase alphanumeric with hyphens, no leading/trailing hyphens).
func validatePluginName(name string) error {
	if name == "" {
		return fmt.Errorf("plugin name must not be empty")
	}
	if !kebabCaseRe.MatchString(name) {
		return fmt.Errorf("plugin name %q must be kebab-case (lowercase alphanumeric with hyphens, no leading/trailing hyphens)", name)
	}
	return nil
}

// ParsePluginManifest unmarshals JSON plugin manifest data and validates
// the required name field.
func ParsePluginManifest(data []byte) (PluginManifest, error) {
	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return PluginManifest{}, fmt.Errorf("parsing plugin manifest: %w", err)
	}
	if err := validatePluginName(m.Name); err != nil {
		return PluginManifest{}, fmt.Errorf("invalid plugin manifest: %w", err)
	}
	return m, nil
}

// expandPluginRoot replaces plugin-root placeholders with pluginDir in s.
func expandPluginRoot(s string, pluginDir string) string {
	s = strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
	return strings.ReplaceAll(s, "${PLUGIN_ROOT}", pluginDir)
}

// LoadedPlugin represents a plugin that has been loaded from disk.
type LoadedPlugin struct {
	Manifest   PluginManifest
	Dir        string                         // absolute path = CLAUDE_PLUGIN_ROOT
	Skills     map[string]SkillMeta           // namespaced as "plugin-name:skill-name"
	Agents     map[string]PluginAgent         // namespaced as "plugin-name:agent-name"
	Hooks      map[HookEvent][]RegisteredHook // keyed by event type
	MCPConfigs []MCPServerConfig              // namespaced as "plugin_<name>_<server>"
}

// discoverPluginSkills scans a plugin's skills directories and returns
// skills namespaced as "pluginName:skillName".
func discoverPluginSkills(pluginDir, pluginName string) map[string]SkillMeta {
	dirs := resolveComponentDirs(pluginDir, "skills", nil)
	raw := map[string]SkillMeta{}
	for _, dir := range dirs {
		scanSkillsDir(dir, raw)
	}
	namespaced := make(map[string]SkillMeta, len(raw))
	for name, meta := range raw {
		namespaced[pluginName+":"+name] = meta
	}
	return namespaced
}

// discoverPluginMCPConfigs reads MCP server configs from a plugin's .mcp.json
// file and/or inline manifest mcpServers field. Server names are prefixed with
// "plugin_<pluginName>_" and ${CLAUDE_PLUGIN_ROOT} is expanded to pluginDir.
func discoverPluginMCPConfigs(pluginDir string, manifestMCPServers json.RawMessage, pluginName string) ([]MCPServerConfig, error) {
	var layers [][]MCPServerConfig

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
			inlineConfigs, err := parseMCPServerMap(servers, "inline")
			if err != nil {
				return nil, err
			}
			layers = append(layers, inlineConfigs)
		}
	}

	merged := MergeMCPConfigs(layers...)
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
func loadPluginMCPFile(path, pluginDir string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand ${CLAUDE_PLUGIN_ROOT} before env-var expansion so
	// expandEnvVars (called by serverJSONToConfig) doesn't fail on it.
	expanded := expandPluginRoot(string(data), pluginDir)

	var cf struct {
		// serf:naming-ignore — mirrors Claude .mcp.json upstream format
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(expanded), &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	return parseMCPServerMap(cf.MCPServers, path)
}

// parseMCPServerMap converts a map of server names to raw JSON into
// MCPServerConfig slices. The source string is used for error context.
func parseMCPServerMap(servers map[string]json.RawMessage, source string) ([]MCPServerConfig, error) {
	var configs []MCPServerConfig
	for name, raw := range servers {
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, source, err)
		}
		cfg, err := serverJSONToConfig(name, sj)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, source, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// LoadPlugin reads a plugin manifest from <dir>/.codex-plugin/plugin.json
// or <dir>/.claude-plugin/plugin.json, parses it, and returns a LoadedPlugin
// with Dir set to the resolved absolute path.
func LoadPlugin(dir string) (LoadedPlugin, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("resolving plugin dir %q: %w", dir, err)
	}

	manifestPath := filepath.Join(resolved, ".codex-plugin", "plugin.json")
	if _, err := os.Stat(manifestPath); err != nil {
		if !os.IsNotExist(err) {
			return LoadedPlugin{}, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
		}
		manifestPath = filepath.Join(resolved, ".claude-plugin", "plugin.json")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("reading plugin manifest %q: %w", manifestPath, err)
	}

	manifest, err := ParsePluginManifest(data)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}

	lp := LoadedPlugin{Manifest: manifest, Dir: resolved}
	lp.Skills = discoverPluginSkills(resolved, manifest.Name)

	agents, err := discoverPluginAgents(resolved, manifest.Agents, manifest.Name)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Agents = agents

	hooks, err := discoverPluginHooks(resolved, manifest.Hooks, manifest.Name)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.Hooks = hooks

	mcpConfigs, err := discoverPluginMCPConfigs(resolved, manifest.MCPServers, manifest.Name)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.MCPConfigs = mcpConfigs

	return lp, nil
}

// LoadPlugins loads plugins from multiple directories and checks for
// duplicate plugin names.
func LoadPlugins(dirs []string) ([]LoadedPlugin, error) {
	plugins := make([]LoadedPlugin, 0, len(dirs))
	seen := make(map[string]string) // name -> dir

	for _, dir := range dirs {
		lp, err := LoadPlugin(dir)
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
