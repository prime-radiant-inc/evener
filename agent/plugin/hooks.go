package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// validHookEvents is the set of event names that serf fires today.
// An event in recognizedClaudeEvents but not here is "recognized but unsupported."
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

// recognizedClaudeEvents is the full set of event names documented by Claude Code
// (https://code.claude.com/docs/en/hooks). Events here that are not in validHookEvents
// are reserved-placeholder: parsed and diagnosed, but never fired by serf.
// Tier: serf-native (the recognition and classification machinery).
var recognizedClaudeEvents = map[HookEvent]bool{
	// Implemented by serf (also in validHookEvents):
	HookPreToolUse:       true,
	HookPostToolUse:      true,
	HookStop:             true,
	HookSubagentStop:     true,
	HookUserPromptSubmit: true,
	HookSessionStart:     true,
	HookSessionEnd:       true,
	HookPreCompact:       true,
	HookNotification:     true,
	// reserved-placeholder: recognized but not yet fired by serf:
	"Setup":               true,
	"InstructionsLoaded":  true,
	"UserPromptExpansion": true,
	"MessageDisplay":      true,
	"PermissionRequest":   true,
	"PostToolUseFailure":  true,
	"PostToolBatch":       true,
	"PermissionDenied":    true,
	"SubagentStart":       true,
	"TaskCreated":         true,
	"TaskCompleted":       true,
	"StopFailure":         true,
	"TeammateIdle":        true,
	"ConfigChange":        true,
	"CwdChanged":          true,
	"FileChanged":         true,
	"WorktreeCreate":      true,
	"WorktreeRemove":      true,
	"PostCompact":         true,
	"Elicitation":         true,
	"ElicitationResult":   true,
}

// hookEventTier maps each known event to its compatibility tier.
// Events in validHookEvents are "claude-compatible-subset" (serf fires them);
// recognized-but-unsupported Claude events are "reserved-placeholder."
// Tier: serf-native (the tier registry itself).
var hookEventTier = func() map[HookEvent]string {
	m := make(map[HookEvent]string, len(recognizedClaudeEvents))
	for e := range recognizedClaudeEvents {
		if validHookEvents[e] {
			m[e] = "claude-compatible-subset"
		} else {
			m[e] = "reserved-placeholder"
		}
	}
	return m
}()

// EventTier returns the compatibility tier label for a hook event.
// Events serf currently fires return "claude-compatible-subset";
// Claude-documented events not yet fired by serf return "reserved-placeholder";
// unknown events return an empty string.
// Tier: serf-native.
func EventTier(e HookEvent) string {
	return hookEventTier[e]
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

	// claude-compatible-subset: official Claude handler fields (07 §"Formal config shape").
	// Args is the exec-form argument list; when non-empty the command is spawned directly
	// without shell interpretation (Shell is ignored in that case).
	Args []string
	// Shell selects the execution shell for shell-form commands; "" and "bash" are
	// equivalent. "powershell" is a reserved-placeholder; other values are errors.
	Shell string
	// If is an optional permission-rule filter for tool events (parse-only; not enforced
	// until Phase B).
	If string
	// Async marks the command as fire-and-forget (parse-only; execution deferred to
	// Phase C).
	Async bool
	// AsyncRewake implies Async and requests a re-wake turn on completion (parse-only;
	// execution deferred to Phase C).
	AsyncRewake bool
	// StatusMessage is optional user-visible status text while the hook runs (captured;
	// surfacing deferred).
	StatusMessage string

	// serf-native: source metadata threaded by the parser for diagnostics.
	// SourcePath is the resolved file path for file-based hooks; empty for inline configs.
	SourcePath string
	// Event is the hook event this handler belongs to.
	Event HookEvent
	// GroupIndex is the index of the matcher group within the event array.
	GroupIndex int
	// HandlerIndex is the index of this handler within its matcher group.
	HandlerIndex int
	// UnknownFields captures unrecognized handler keys for diagnostics and forward
	// compatibility. Keys present in the official Claude config shape are excluded.
	UnknownFields map[string]json.RawMessage
}

// hookMatcherGroup is the JSON shape of a matcher group within an event.
// Hooks is kept as raw bytes so each handler's unrecognized keys can be
// recovered for diagnostics (encoding/json drops them when decoding into the
// typed hookSpec).
type hookMatcherGroup struct {
	Matcher string            `json:"matcher"`
	Hooks   []json.RawMessage `json:"hooks"`
}

// hookSpec is the JSON shape of an individual hook action.
// Field names and json tags match Claude's documented config shape
// (07 §"Formal config shape").
type hookSpec struct {
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
	Model   string   `json:"model,omitempty"`
	Args    []string `json:"args,omitempty"`
	Shell   string   `json:"shell,omitempty"`
	If      string   `json:"if,omitempty"`
	Async   bool     `json:"async,omitempty"`
	// serf:naming-ignore
	AsyncRewake bool `json:"asyncRewake,omitempty"` // Claude wire format: camelCase required
	// serf:naming-ignore
	StatusMessage string `json:"statusMessage,omitempty"` // Claude wire format: camelCase required
}

// knownHookSpecKeys is the set of json field names in hookSpec, used to
// populate RegisteredHook.UnknownFields.
var knownHookSpecKeys = map[string]bool{
	"type": true, "command": true, "prompt": true, "timeout": true, "model": true,
	"args": true, "shell": true, "if": true, "async": true, "asyncRewake": true,
	"statusMessage": true,
}

// parsePluginHooks parses hook configuration from JSON data.
// Accepts wrapper format ({"hooks": {...}}) or direct format (events at top level).
// Expands plugin-root placeholders in Command and Prompt fields.
// This is a thin wrapper over parsePluginHooksDiag for callers that do not need
// the recognized-but-unsupported and unknown event diagnostics.
func parsePluginHooks(data []byte, pluginDir, pluginName string) (map[HookEvent][]RegisteredHook, error) {
	hooks, _, _, err := parsePluginHooksDiag(data, pluginDir, pluginName)
	return hooks, err
}

// parsePluginHooksDiag parses hook configuration from JSON data and classifies
// event names into three buckets:
//   - supported: returned in the hooks map (events in validHookEvents)
//   - unsupported: in recognizedClaudeEvents but not validHookEvents (reserved-placeholder)
//   - unknown: not recognized as a Claude event at all
//
// The sourcePath parameter is threaded into each RegisteredHook for diagnostics;
// pass the resolved file path for file-based hooks, empty for inline configs.
// Tier: serf-native (classification machinery); claude-compatible-subset (field parsing).
func parsePluginHooksDiag(data []byte, pluginDir, pluginName string) (hooks map[HookEvent][]RegisteredHook, unsupported map[HookEvent]bool, unknown map[string]bool, err error) {
	return parsePluginHooksDiagWithSource(data, pluginDir, pluginName, "")
}

// parsePluginHooksDiagWithSource is the full implementation used internally and
// by discoverPluginHooks (which knows the resolved file path).
func parsePluginHooksDiagWithSource(data []byte, pluginDir, pluginName, sourcePath string) (hooks map[HookEvent][]RegisteredHook, unsupported map[HookEvent]bool, unknown map[string]bool, err error) {
	// Try wrapper format first: {"hooks": {...}} or {"description": "...", "hooks": {...}}
	var wrapper struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err = json.Unmarshal(data, &wrapper); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing hooks JSON: %w", err)
	}

	var eventsRaw map[string]json.RawMessage
	if wrapper.Hooks != nil {
		if err = json.Unmarshal(wrapper.Hooks, &eventsRaw); err != nil {
			return nil, nil, nil, fmt.Errorf("parsing hooks wrapper: %w", err)
		}
	} else {
		// Direct format: events at top level
		if err = json.Unmarshal(data, &eventsRaw); err != nil {
			return nil, nil, nil, fmt.Errorf("parsing hooks direct format: %w", err)
		}
		// In direct format, drop meta keys that are not event names so they are not
		// misclassified as unknown events: "description" plus any "$"-prefixed key
		// (e.g. the common "$schema"). Recognized event names are never dropped.
		for k := range eventsRaw {
			if recognizedClaudeEvents[HookEvent(k)] {
				continue
			}
			if k == "description" || strings.HasPrefix(k, "$") {
				delete(eventsRaw, k)
			}
		}
	}

	hooks = make(map[HookEvent][]RegisteredHook)
	unsupported = make(map[HookEvent]bool)
	unknown = make(map[string]bool)

	for eventName, raw := range eventsRaw {
		event := HookEvent(eventName)
		if validHookEvents[event] {
			var groups []hookMatcherGroup
			if err = json.Unmarshal(raw, &groups); err != nil {
				return nil, nil, nil, fmt.Errorf("parsing hooks for event %q: %w", eventName, err)
			}

			for gi, rawHandlers := range groups {
				for hi, rawHandler := range rawHandlers.Hooks {
					var spec hookSpec
					if err = json.Unmarshal(rawHandler, &spec); err != nil {
						return nil, nil, nil, fmt.Errorf("parsing handler %d in event %q: %w", hi, eventName, err)
					}

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
						Matcher:       rawHandlers.Matcher,
						Type:          spec.Type,
						Command:       expandPluginRoot(spec.Command, pluginDir),
						Prompt:        expandPluginRoot(spec.Prompt, pluginDir),
						Timeout:       timeout,
						Model:         spec.Model,
						PluginName:    pluginName,
						PluginDir:     pluginDir,
						Args:          expandPluginRootArgs(spec.Args, pluginDir),
						Shell:         spec.Shell,
						If:            spec.If,
						Async:         spec.Async,
						AsyncRewake:   spec.AsyncRewake,
						StatusMessage: spec.StatusMessage,
						SourcePath:    sourcePath,
						Event:         event,
						GroupIndex:    gi,
						HandlerIndex:  hi,
						UnknownFields: captureUnknownFields(rawHandler),
					}
					hooks[event] = append(hooks[event], rh)
				}
			}
		} else if recognizedClaudeEvents[event] {
			unsupported[event] = true
		} else {
			unknown[eventName] = true
		}
	}

	return hooks, unsupported, unknown, nil
}

// captureUnknownFields parses the RAW handler JSON into a key→value map and
// returns any keys not present in knownHookSpecKeys, for forward-compatible
// diagnostics. It must read the original bytes — encoding/json silently drops
// unknown keys when decoding into the typed hookSpec, so a re-marshaled spec can
// never recover them.
func captureUnknownFields(rawHandler json.RawMessage) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(rawHandler, &all); err != nil {
		return nil
	}
	var result map[string]json.RawMessage
	for k, v := range all {
		if !knownHookSpecKeys[k] {
			if result == nil {
				result = make(map[string]json.RawMessage)
			}
			result[k] = v
		}
	}
	return result
}

// expandPluginRootArgs applies expandPluginRoot to each exec-form argument so
// ${CLAUDE_PLUGIN_ROOT}/${PLUGIN_ROOT} are substituted the same way as in the
// Command and Prompt strings. Exec form has no shell, so without this the
// placeholder would reach the program literally. Returns nil for an empty list
// to preserve the "shell form" signal (len(Args)==0).
func expandPluginRootArgs(args []string, pluginDir string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = expandPluginRoot(a, pluginDir)
	}
	return out
}

// discoverPluginHooksDiag finds and parses hook configuration for a plugin,
// returning the supported hooks map alongside the unsupported and unknown sets
// for diagnostics. If manifestHooks is a JSON string, it is treated as a path
// to a hooks file. If manifestHooks is a JSON object, it is parsed inline.
// Otherwise, reads <pluginDir>/hooks/hooks.json if it exists.
// The resolved file path is threaded into each RegisteredHook.SourcePath.
func discoverPluginHooksDiag(pluginDir string, manifestHooks json.RawMessage, pluginName string) (hooks map[HookEvent][]RegisteredHook, unsupported map[HookEvent]bool, unknown map[string]bool, err error) {
	if len(manifestHooks) > 0 {
		trimmed := bytes.TrimLeft(manifestHooks, " \t\n\r")
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// String value: path to hooks file
			var path string
			if err = json.Unmarshal(manifestHooks, &path); err != nil {
				return nil, nil, nil, fmt.Errorf("parsing hooks path: %w", err)
			}
			path = expandPluginRoot(path, pluginDir)
			if !filepath.IsAbs(path) {
				path = filepath.Join(pluginDir, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("reading hooks file %q: %w", path, err)
			}
			return parsePluginHooksDiagWithSource(data, pluginDir, pluginName, path)
		}
		if len(trimmed) > 0 && trimmed[0] == '{' {
			// Object value: inline hooks config (no file path, SourcePath stays empty)
			return parsePluginHooksDiag(manifestHooks, pluginDir, pluginName)
		}
	}

	// Default: read <pluginDir>/hooks/hooks.json
	defaultPath := filepath.Join(pluginDir, "hooks", "hooks.json")
	data, err := os.ReadFile(defaultPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[HookEvent][]RegisteredHook{}, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("reading hooks file %q: %w", defaultPath, err)
	}
	return parsePluginHooksDiagWithSource(data, pluginDir, pluginName, defaultPath)
}

// discoverPluginHooks finds and parses hook configuration for a plugin.
// It is a thin wrapper over discoverPluginHooksDiag for callers that do not
// need the unsupported/unknown diagnostic sets.
func discoverPluginHooks(pluginDir string, manifestHooks json.RawMessage, pluginName string) (map[HookEvent][]RegisteredHook, error) {
	hooks, _, _, err := discoverPluginHooksDiag(pluginDir, manifestHooks, pluginName)
	return hooks, err
}
