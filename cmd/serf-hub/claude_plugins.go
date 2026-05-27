package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/launchconfig"
)

type claudeSettingsFile struct {
	EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
}

func defaultHubLaunchDefaults() launchconfig.Layer {
	return launchconfig.Layer{PluginDirs: defaultEnabledClaudePluginDirs()}
}

func defaultEnabledClaudePluginDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return enabledClaudePluginDirs(home)
}

func enabledClaudePluginDirs(home string) []string {
	if home == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return nil
	}
	var settings claudeSettingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	if len(settings.EnabledPlugins) == 0 {
		return nil
	}

	keys := make([]string, 0, len(settings.EnabledPlugins))
	for key := range settings.EnabledPlugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	seen := map[string]bool{}
	var dirs []string
	for _, key := range keys {
		enabled, version := parseClaudePluginEnabled(settings.EnabledPlugins[key])
		if !enabled {
			continue
		}
		plugin, marketplace, ok := splitClaudePluginKey(key)
		if !ok {
			continue
		}
		base := filepath.Join(home, ".claude", "plugins", "cache", marketplace, plugin)
		var dir string
		if version != "" {
			dir = filepath.Join(base, version)
			if !pluginManifestExists(dir) {
				continue
			}
		} else {
			dir = newestClaudePluginCacheDir(base)
			if dir == "" {
				continue
			}
		}
		if !pluginHasSessionStartHook(dir) {
			continue
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func parseClaudePluginEnabled(raw json.RawMessage) (bool, string) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, ""
	}
	var version string
	if err := json.Unmarshal(raw, &version); err == nil && strings.TrimSpace(version) != "" {
		return true, strings.TrimSpace(version)
	}
	var obj struct {
		Enabled *bool  `json:"enabled"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false, ""
	}
	if obj.Enabled != nil && !*obj.Enabled {
		return false, ""
	}
	return true, strings.TrimSpace(obj.Version)
}

func splitClaudePluginKey(key string) (plugin string, marketplace string, ok bool) {
	idx := strings.LastIndex(key, "@")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", false
	}
	return key[:idx], key[idx+1:], true
}

func newestClaudePluginCacheDir(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		if pluginManifestExists(dir) {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Slice(names, func(i, j int) bool {
		return pluginVersionLess(names[i], names[j])
	})
	return filepath.Join(base, names[len(names)-1])
}

func pluginVersionLess(a, b string) bool {
	if a == b {
		return false
	}
	if a == "unknown" {
		return b != "unknown"
	}
	if b == "unknown" {
		return false
	}
	return a < b
}

func pluginManifestExists(dir string) bool {
	for _, subdir := range []string{".codex-plugin", ".claude-plugin"} {
		if _, err := os.Stat(filepath.Join(dir, subdir, "plugin.json")); err == nil {
			return true
		}
	}
	return false
}

func pluginHasSessionStartHook(dir string) bool {
	plugin, err := agent.LoadPlugin(dir)
	if err != nil {
		return false
	}
	return len(plugin.Hooks[agent.HookSessionStart]) > 0
}
