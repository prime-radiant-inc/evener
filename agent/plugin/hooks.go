package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	// HookPreToolUse fires before a tool is used.
	HookPreToolUse HookEvent = "PreToolUse"
	// HookPostToolUse fires after a tool is used.
	HookPostToolUse HookEvent = "PostToolUse"
	// HookStop fires when the session stops.
	HookStop HookEvent = "Stop"
	// HookSubagentStop fires when a subagent stops.
	HookSubagentStop HookEvent = "SubagentStop"
	// HookUserPromptSubmit fires when a user prompt is submitted.
	HookUserPromptSubmit HookEvent = "UserPromptSubmit"
	// HookSessionStart fires when a session starts.
	HookSessionStart HookEvent = "SessionStart"
	// HookSessionEnd fires when a session ends.
	HookSessionEnd HookEvent = "SessionEnd"
	// HookPreCompact fires before compaction.
	HookPreCompact HookEvent = "PreCompact"
	// HookNotification fires on a notification.
	HookNotification HookEvent = "Notification"
)

// SessionStartKind is the matcher target used for SessionStart hooks.
// Claude Code-style hooks distinguish ordinary startup from resume, clear,
// and compaction lifecycle boundaries.
type SessionStartKind string

const (
	// SessionStartKindStartup targets ordinary session startup.
	SessionStartKindStartup SessionStartKind = "startup"
	// SessionStartKindResume targets session resume.
	SessionStartKindResume SessionStartKind = "resume"
	// SessionStartKindClear targets session clear.
	SessionStartKindClear SessionStartKind = "clear"
	// SessionStartKindCompact targets the compaction lifecycle boundary.
	SessionStartKindCompact SessionStartKind = "compact"
)

// validHookEvents is the set of recognized event names.
var validHookEvents = map[HookEvent]bool{
	HookPreToolUse:       true,
	HookPostToolUse:      true,
	HookStop:             true,
	HookSubagentStop:     true,
	HookUserPromptSubmit: true,
	HookSessionStart:     true,
	HookSessionEnd:       true,
	HookPreCompact:       true,
	HookNotification:     true,
}

// RegisteredHook describes a single hook action within a matcher group.
type RegisteredHook struct {
	Matcher    string // regex pattern, "*" = match all
	Type       string // "command" or "prompt"
	Command    string // for command hooks
	Prompt     string // for prompt hooks
	Timeout    int    // seconds (default: 60 for command, 30 for prompt)
	Model      string // for prompt hooks (optional)
	PluginName string // name of the plugin that registered this hook
	PluginDir  string // CLAUDE_PLUGIN_ROOT
}

// hookMatcherGroup is the JSON shape of a matcher group within an event.
type hookMatcherGroup struct {
	Matcher string     `json:"matcher"`
	Hooks   []hookSpec `json:"hooks"`
}

// hookSpec is the JSON shape of an individual hook action.
type hookSpec struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Model   string `json:"model,omitempty"`
}

// parsePluginHooks parses hook configuration from JSON data.
// Accepts wrapper format ({"hooks": {...}}) or direct format (events at top level).
// Expands plugin-root placeholders in Command and Prompt fields.
func parsePluginHooks(data []byte, pluginDir, pluginName string) (map[HookEvent][]RegisteredHook, error) {
	// Try wrapper format first: {"hooks": {...}} or {"description": "...", "hooks": {...}}
	var wrapper struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing hooks JSON: %w", err)
	}

	var eventsRaw map[string]json.RawMessage
	if wrapper.Hooks != nil {
		if err := json.Unmarshal(wrapper.Hooks, &eventsRaw); err != nil {
			return nil, fmt.Errorf("parsing hooks wrapper: %w", err)
		}
	} else {
		// Direct format: events at top level
		if err := json.Unmarshal(data, &eventsRaw); err != nil {
			return nil, fmt.Errorf("parsing hooks direct format: %w", err)
		}
		// Remove non-event keys (like "description")
		for k := range eventsRaw {
			if !validHookEvents[HookEvent(k)] {
				delete(eventsRaw, k)
			}
		}
	}

	result := make(map[HookEvent][]RegisteredHook)

	for eventName, raw := range eventsRaw {
		event := HookEvent(eventName)
		if !validHookEvents[event] {
			continue
		}

		var groups []hookMatcherGroup
		if err := json.Unmarshal(raw, &groups); err != nil {
			return nil, fmt.Errorf("parsing hooks for event %q: %w", eventName, err)
		}

		for _, g := range groups {
			for _, spec := range g.Hooks {
				timeout := spec.Timeout
				if timeout == 0 {
					switch spec.Type {
					case "command":
						timeout = 60
					case "prompt":
						timeout = 30
					}
				}

				rh := RegisteredHook{
					Matcher:    g.Matcher,
					Type:       spec.Type,
					Command:    expandPluginRoot(spec.Command, pluginDir),
					Prompt:     expandPluginRoot(spec.Prompt, pluginDir),
					Timeout:    timeout,
					Model:      spec.Model,
					PluginName: pluginName,
					PluginDir:  pluginDir,
				}
				result[event] = append(result[event], rh)
			}
		}
	}

	return result, nil
}

// discoverPluginHooks finds and parses hook configuration for a plugin.
// If manifestHooks is a JSON string, it is treated as a path to a hooks file.
// If manifestHooks is a JSON object, it is parsed inline.
// Otherwise, reads <pluginDir>/hooks/hooks.json if it exists.
func discoverPluginHooks(pluginDir string, manifestHooks json.RawMessage, pluginName string) (map[HookEvent][]RegisteredHook, error) {
	if len(manifestHooks) > 0 {
		trimmed := bytes.TrimLeft(manifestHooks, " \t\n\r")
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// String value: path to hooks file
			var path string
			if err := json.Unmarshal(manifestHooks, &path); err != nil {
				return nil, fmt.Errorf("parsing hooks path: %w", err)
			}
			path = expandPluginRoot(path, pluginDir)
			if !filepath.IsAbs(path) {
				path = filepath.Join(pluginDir, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading hooks file %q: %w", path, err)
			}
			return parsePluginHooks(data, pluginDir, pluginName)
		}
		if len(trimmed) > 0 && trimmed[0] == '{' {
			// Object value: inline hooks config
			return parsePluginHooks(manifestHooks, pluginDir, pluginName)
		}
	}

	// Default: read <pluginDir>/hooks/hooks.json
	defaultPath := filepath.Join(pluginDir, "hooks", "hooks.json")
	data, err := os.ReadFile(defaultPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[HookEvent][]RegisteredHook{}, nil
		}
		return nil, fmt.Errorf("reading hooks file %q: %w", defaultPath, err)
	}
	return parsePluginHooks(data, pluginDir, pluginName)
}
