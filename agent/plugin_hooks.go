package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/llm"
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

// trimJSONWhitespace returns the input with leading JSON whitespace removed.
func trimJSONWhitespace(data []byte) []byte {
	return bytes.TrimLeft(data, " \t\n\r")
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

// PromptHookClient is the interface for LLM calls used by prompt hooks.
type PromptHookClient interface {
	Generate(ctx context.Context, req llm.Request) (llm.Response, error)
}

// clientAdapter wraps *llm.Client to satisfy PromptHookClient by delegating
// Generate to the Client's Complete method.
type clientAdapter struct {
	client *llm.Client
}

func (a clientAdapter) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.client.Complete(ctx, req)
}

// substituteHookVariables replaces $TOOL_INPUT, $TOOL_RESULT, $USER_PROMPT,
// and $TOOL_NAME placeholders in a prompt string with values from the input.
func substituteHookVariables(prompt string, input HookInput) string {
	// $TOOL_INPUT -> JSON-encoded ToolInput or "null"
	toolInputStr := "null"
	if input.ToolInput != nil {
		if b, err := json.Marshal(input.ToolInput); err == nil {
			toolInputStr = string(b)
		}
	}

	// $TOOL_RESULT -> ToolResult or "null"
	toolResultStr := "null"
	if input.ToolResult != "" {
		toolResultStr = input.ToolResult
	}

	// $USER_PROMPT -> UserPrompt or "null"
	userPromptStr := "null"
	if input.UserPrompt != "" {
		userPromptStr = input.UserPrompt
	}

	r := strings.NewReplacer(
		"$TOOL_INPUT", toolInputStr,
		"$TOOL_RESULT", toolResultStr,
		"$USER_PROMPT", userPromptStr,
		"$TOOL_NAME", input.ToolName,
	)
	return r.Replace(prompt)
}

// executePromptHook runs a prompt hook by calling the LLM with the substituted prompt.
func executePromptHook(ctx context.Context, client PromptHookClient, model string, hook RegisteredHook, input HookInput) (HookResult, error) {
	prompt := substituteHookVariables(hook.Prompt, input)

	reqModel := model
	if hook.Model != "" {
		reqModel = hook.Model
	}

	req := llm.Request{
		Model: reqModel,
		Messages: []llm.Message{
			llm.User(prompt),
		},
	}

	resp, err := client.Generate(ctx, req)
	if err != nil {
		return HookResult{}, fmt.Errorf("prompt hook LLM call: %w", err)
	}

	text := resp.Message.Text()
	return HookResult{Stdout: text, ExitCode: 0}, nil
}

// HookRunner orchestrates hook matching and parallel dispatch.
type HookRunner struct {
	hooks   map[HookEvent][]RegisteredHook
	client  PromptHookClient
	model   string
	onEvent func(EventKind, any) // optional event callback
}

// SetEventCallback sets an optional callback that is invoked for hook
// lifecycle events (HookStart, HookEnd).
func (r *HookRunner) SetEventCallback(fn func(EventKind, any)) {
	r.onEvent = fn
}

// NewHookRunner creates a HookRunner with the given LLM client and default model
// for prompt hooks.
func NewHookRunner(client PromptHookClient, model string) *HookRunner {
	return &HookRunner{
		hooks:  make(map[HookEvent][]RegisteredHook),
		client: client,
		model:  model,
	}
}

// Add registers hooks for an event.
func (r *HookRunner) Add(event HookEvent, hooks ...RegisteredHook) {
	r.hooks[event] = append(r.hooks[event], hooks...)
}

// matchHooks returns hooks registered for the event whose Matcher matches toolName.
// "*" matches everything; other matchers are compiled as regex.
func (r *HookRunner) matchHooks(event HookEvent, toolName string) []RegisteredHook {
	var matched []RegisteredHook
	for _, hook := range r.hooks[event] {
		if hook.Matcher == "*" {
			matched = append(matched, hook)
			continue
		}
		re, err := regexp.Compile(hook.Matcher)
		if err != nil {
			continue // skip invalid regex
		}
		if re.MatchString(toolName) {
			matched = append(matched, hook)
		}
	}
	return matched
}

// HookRunResult contains the aggregated output from running hooks.
type HookRunResult struct {
	SystemMessages []string
}

// PreToolUseResult contains aggregated output from PreToolUse hooks.
type PreToolUseResult struct {
	Denied         bool
	DenyMessage    string
	SystemMessages []string
	UpdatedInput   map[string]any
}

// StopResult contains aggregated output from Stop/SubagentStop hooks.
type StopResult struct {
	Blocked        bool
	BlockReason    string
	SystemMessages []string
}

// ParsedHookOutput is the structured interpretation of a hook's stdout and exit code.
type ParsedHookOutput struct {
	Continue       bool
	SuppressOutput bool
	SystemMessage  string
	Denied         bool
	UpdatedInput   map[string]any
	Blocked        bool
	BlockReason    string
	IsError        bool
	RawExitCode    int
}

// parseHookOutput interprets hook stdout and exit code into structured output.
func parseHookOutput(stdout string, exitCode int) ParsedHookOutput {
	result := ParsedHookOutput{Continue: true, RawExitCode: exitCode}

	if exitCode == 2 {
		result.IsError = true
		result.SystemMessage = stdout
		return result
	}

	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return result
	}

	// Try JSON parse
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		// Plain text: treat as system message
		result.SystemMessage = stdout
		return result
	}

	// Extract standard fields
	if v, ok := parsed["continue"]; ok {
		if b, ok := v.(bool); ok {
			result.Continue = b
		}
	}
	if v, ok := parsed["suppressOutput"]; ok {
		if b, ok := v.(bool); ok {
			result.SuppressOutput = b
		}
	}
	if v, ok := parsed["systemMessage"]; ok {
		if s, ok := v.(string); ok {
			result.SystemMessage = s
		}
	}

	// Extract hookSpecificOutput
	if hso, ok := parsed["hookSpecificOutput"].(map[string]any); ok {
		if pd, ok := hso["permissionDecision"].(string); ok && pd == "deny" {
			result.Denied = true
			if reason, ok := hso["reason"].(string); ok {
				if result.SystemMessage == "" {
					result.SystemMessage = reason
				}
			}
		}
		if ui, ok := hso["updatedInput"].(map[string]any); ok {
			result.UpdatedInput = ui
		}
		// SessionStart hooks inject context via additionalContext.
		if ac, ok := hso["additionalContext"].(string); ok && ac != "" {
			if result.SystemMessage == "" {
				result.SystemMessage = ac
			} else {
				result.SystemMessage += "\n" + ac
			}
		}
	}

	// Extract decision/reason for Stop hooks
	if decision, ok := parsed["decision"].(string); ok && decision == "block" {
		result.Blocked = true
		if reason, ok := parsed["reason"].(string); ok {
			result.BlockReason = reason
		}
	}

	return result
}

// runHook executes a single hook (command or prompt) and returns the parsed output.
func (r *HookRunner) runHook(ctx context.Context, hook RegisteredHook, input HookInput) ParsedHookOutput {
	var hr HookResult
	var err error

	switch hook.Type {
	case "command":
		hr, err = executeCommandHook(ctx, hook, input)
	case "prompt":
		if r.client == nil {
			return ParsedHookOutput{Continue: true, SystemMessage: "prompt hook skipped: no LLM client"}
		}
		hr, err = executePromptHook(ctx, r.client, r.model, hook, input)
	default:
		return ParsedHookOutput{Continue: true}
	}

	if err != nil {
		return ParsedHookOutput{Continue: true, IsError: true, SystemMessage: err.Error()}
	}

	// Per Claude Code spec: exit code 2 means stderr is fed back to Claude.
	output := hr.Stdout
	if hr.ExitCode == 2 && strings.TrimSpace(hr.Stderr) != "" {
		output = hr.Stderr
	}
	return parseHookOutput(output, hr.ExitCode)
}

// runAll executes all matched hooks in parallel and returns their parsed outputs.
func (r *HookRunner) runAll(ctx context.Context, event HookEvent, toolName string, input HookInput) []ParsedHookOutput {
	claudeName := MapSerfToolNameToClaude(toolName)
	matched := r.matchHooks(event, claudeName)
	if len(matched) == 0 {
		return nil
	}

	results := make([]ParsedHookOutput, len(matched))
	var wg sync.WaitGroup
	wg.Add(len(matched))

	for i, hook := range matched {
		go func(idx int, h RegisteredHook) {
			defer wg.Done()
			if r.onEvent != nil {
				r.onEvent(EventHookStart, HookStartData{
					Event:      string(event),
					HookType:   h.Type,
					Matcher:    h.Matcher,
					PluginName: h.PluginName,
				})
			}
			start := time.Now()
			results[idx] = r.runHook(ctx, h, input)
			elapsed := time.Since(start)
			if r.onEvent != nil {
				r.onEvent(EventHookEnd, HookEndData{
					Event:      string(event),
					HookType:   h.Type,
					Matcher:    h.Matcher,
					PluginName: h.PluginName,
					ExitCode:   results[idx].RawExitCode,
					DurationMS: elapsed.Milliseconds(),
				})
			}
		}(i, hook)
	}

	wg.Wait()
	return results
}

// RunPreToolUse dispatches PreToolUse hooks and aggregates results.
// Any deny from any hook means denied.
func (r *HookRunner) RunPreToolUse(ctx context.Context, input HookInput) PreToolUseResult {
	outputs := r.runAll(ctx, HookPreToolUse, input.ToolName, input)
	var result PreToolUseResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.Denied {
			result.Denied = true
			if result.DenyMessage == "" {
				result.DenyMessage = o.SystemMessage
			}
		}
		if o.UpdatedInput != nil {
			result.UpdatedInput = o.UpdatedInput
		}
	}
	return result
}

// RunPostToolUse dispatches PostToolUse hooks and aggregates system messages.
func (r *HookRunner) RunPostToolUse(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookPostToolUse, input.ToolName, input)
	return collectSystemMessages(outputs)
}

// RunStop dispatches Stop hooks and aggregates results.
// Any block from any hook means blocked.
func (r *HookRunner) RunStop(ctx context.Context, input HookInput) StopResult {
	return r.runStopEvent(ctx, HookStop, input)
}

// RunSubagentStop dispatches SubagentStop hooks and aggregates results.
func (r *HookRunner) RunSubagentStop(ctx context.Context, input HookInput) StopResult {
	return r.runStopEvent(ctx, HookSubagentStop, input)
}

func (r *HookRunner) runStopEvent(ctx context.Context, event HookEvent, input HookInput) StopResult {
	outputs := r.runAll(ctx, event, input.ToolName, input)
	var result StopResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.Blocked {
			result.Blocked = true
			if result.BlockReason == "" {
				result.BlockReason = o.BlockReason
			}
		}
	}
	return result
}

// RunUserPromptSubmit dispatches UserPromptSubmit hooks.
func (r *HookRunner) RunUserPromptSubmit(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookUserPromptSubmit, "", input)
	return collectSystemMessages(outputs)
}

// RunSessionStart dispatches SessionStart hooks.
// The matchTarget is "startup" to match Claude Code's convention where
// SessionStart matchers match startup types (startup|resume|clear|compact).
func (r *HookRunner) RunSessionStart(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookSessionStart, "startup", input)
	return collectSystemMessages(outputs)
}

// RunSessionEnd dispatches SessionEnd hooks. No return value needed.
func (r *HookRunner) RunSessionEnd(ctx context.Context, input HookInput) {
	r.runAll(ctx, HookSessionEnd, "", input)
}

// RunPreCompact dispatches PreCompact hooks.
func (r *HookRunner) RunPreCompact(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookPreCompact, "", input)
	return collectSystemMessages(outputs)
}

// RunNotification dispatches Notification hooks.
func (r *HookRunner) RunNotification(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookNotification, "", input)
	return collectSystemMessages(outputs)
}

// collectSystemMessages aggregates system messages from parsed outputs.
func collectSystemMessages(outputs []ParsedHookOutput) HookRunResult {
	var result HookRunResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
	}
	return result
}
