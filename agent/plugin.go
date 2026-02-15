package agent

import (
	"encoding/json"
	"fmt"
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
	MCPServers  json.RawMessage `json:"mcpServers,omitempty"`
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

// expandPluginRoot replaces ${CLAUDE_PLUGIN_ROOT} with pluginDir in s.
func expandPluginRoot(s string, pluginDir string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
}
