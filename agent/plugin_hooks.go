package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	HookPreToolUse       HookEvent = "PreToolUse"
	HookPostToolUse      HookEvent = "PostToolUse"
	HookStop             HookEvent = "Stop"
	HookSubagentStop     HookEvent = "SubagentStop"
	HookUserPromptSubmit HookEvent = "UserPromptSubmit"
	HookSessionStart     HookEvent = "SessionStart"
	HookSessionEnd       HookEvent = "SessionEnd"
	HookPreCompact       HookEvent = "PreCompact"
	HookNotification     HookEvent = "Notification"
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
	PluginName string
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

// ParsePluginHooks parses hook configuration from JSON data.
// Accepts wrapper format ({"hooks": {...}}) or direct format (events at top level).
// Expands ${CLAUDE_PLUGIN_ROOT} in Command and Prompt fields.
func ParsePluginHooks(data []byte, pluginDir, pluginName string) (map[HookEvent][]RegisteredHook, error) {
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
		trimmed := trimJSONWhitespace(manifestHooks)
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
			return ParsePluginHooks(data, pluginDir, pluginName)
		}
		if len(trimmed) > 0 && trimmed[0] == '{' {
			// Object value: inline hooks config
			return ParsePluginHooks(manifestHooks, pluginDir, pluginName)
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
	return ParsePluginHooks(data, pluginDir, pluginName)
}

// trimJSONWhitespace returns the input with leading whitespace removed.
func trimJSONWhitespace(data []byte) []byte {
	for i, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return data[i:]
		}
	}
	return nil
}

// HookInput is the JSON payload piped to command hooks via stdin.
type HookInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	ToolResult    string         `json:"tool_result,omitempty"`
	UserPrompt    string         `json:"user_prompt,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

// HookResult captures the output of a hook execution.
type HookResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// executeCommandHook runs a command hook with the given input piped as JSON to stdin.
// Non-zero exit codes are captured in HookResult.ExitCode, not returned as Go errors.
// Only returns an error for infrastructure failures (timeout, process start failure).
func executeCommandHook(ctx context.Context, hook RegisteredHook, input HookInput) (HookResult, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return HookResult{}, fmt.Errorf("marshaling hook input: %w", err)
	}

	timeout := time.Duration(hook.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", hook.Command)
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+hook.PluginDir,
		"CLAUDE_PROJECT_DIR="+input.CWD,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	result := HookResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		// Check if context timed out or was canceled — report as infrastructure error.
		if ctx.Err() != nil {
			return result, fmt.Errorf("hook command killed: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("running hook command: %w", err)
	}

	return result, nil
}
