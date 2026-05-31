package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// hookInput is the JSON payload piped to command hooks via stdin.
type hookInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	ToolResult    string         `json:"tool_result,omitempty"`
	UserPrompt    string         `json:"user_prompt,omitempty"`
	Message       string         `json:"message,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

// hookResult captures the output of a hook execution.
type hookResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// executeCommandHook runs a command hook with the given input piped as JSON to stdin.
// Non-zero exit codes are captured in hookResult.ExitCode, not returned as Go errors.
// Only returns an error for infrastructure failures (timeout, process start failure).
func executeCommandHook(ctx context.Context, hook RegisteredHook, input hookInput) (hookResult, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return hookResult{}, fmt.Errorf("marshaling hook input: %w", err)
	}

	timeout := time.Duration(hook.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", hook.Command)
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+hook.PluginDir,
		"PLUGIN_ROOT="+hook.PluginDir,
		"CLAUDE_PROJECT_DIR="+input.CWD,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	result := hookResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		// Check if context timed out or was canceled — report as infrastructure error.
		if ctx.Err() != nil {
			return result, fmt.Errorf("hook command killed: %w", ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("running hook command: %w", err)
	}

	return result, nil
}

// promptHookClient is the interface for LLM calls used by prompt hooks.
type promptHookClient interface {
	Generate(ctx context.Context, req llm.Request) (llm.Response, error)
}

// clientAdapter wraps *llm.Client to satisfy promptHookClient by delegating
// Generate to the Client's Complete method.
type clientAdapter struct {
	client *llm.Client
}

func (a clientAdapter) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.client.Complete(ctx, req)
}

// substituteHookVariables replaces $TOOL_INPUT, $TOOL_RESULT, $USER_PROMPT,
// and $TOOL_NAME placeholders in a prompt string with values from the input.
func substituteHookVariables(prompt string, input hookInput) string {
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
		"$MESSAGE", input.Message,
		"$TOOL_NAME", input.ToolName,
	)
	return r.Replace(prompt)
}

// executePromptHook runs a prompt hook by calling the LLM with the substituted prompt.
func executePromptHook(ctx context.Context, client promptHookClient, model string, hook RegisteredHook, input hookInput) (hookResult, error) {
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
		return hookResult{}, fmt.Errorf("prompt hook LLM call: %w", err)
	}

	text := resp.Message.Text()
	return hookResult{Stdout: text, ExitCode: 0}, nil
}

// hookRunner orchestrates hook matching and parallel dispatch.
type hookRunner struct {
	hooks   map[HookEvent][]RegisteredHook
	client  promptHookClient
	model   string
	onEvent func(EventKind, any) // optional event callback
}

// SetEventCallback sets an optional callback that is invoked for hook
// lifecycle events (HookStart, HookEnd).
func (r *hookRunner) SetEventCallback(fn func(EventKind, any)) {
	r.onEvent = fn
}

// newHookRunner creates a hookRunner with the given LLM client and default model
// for prompt hooks.
func newHookRunner(client promptHookClient, model string) *hookRunner {
	return &hookRunner{
		hooks:  make(map[HookEvent][]RegisteredHook),
		client: client,
		model:  model,
	}
}

// Summary returns the number of registered hooks per event.
// Events with zero hooks are omitted.
func (r *hookRunner) Summary() map[HookEvent]int {
	out := make(map[HookEvent]int)
	for event, hooks := range r.hooks {
		if len(hooks) > 0 {
			out[event] = len(hooks)
		}
	}
	return out
}

// Add registers hooks for an event.
func (r *hookRunner) Add(event HookEvent, hooks ...RegisteredHook) {
	r.hooks[event] = append(r.hooks[event], hooks...)
}

// matchHooks returns hooks registered for the event whose Matcher matches toolName.
// "*" matches everything; other matchers are compiled as regex.
func (r *hookRunner) matchHooks(event HookEvent, toolName string) []RegisteredHook {
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

// hookRunResult contains the aggregated output from running hooks.
type hookRunResult struct {
	SystemMessages []string
}

// preToolUseResult contains aggregated output from PreToolUse hooks.
type preToolUseResult struct {
	Denied         bool
	DenyMessage    string
	SystemMessages []string
	UpdatedInput   map[string]any
}

// stopResult contains aggregated output from Stop/SubagentStop hooks.
type stopResult struct {
	Blocked        bool
	BlockReason    string
	SystemMessages []string
}

// parsedHookOutput is the structured interpretation of a hook's stdout and exit code.
type parsedHookOutput struct {
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
func parseHookOutput(stdout string, exitCode int) parsedHookOutput {
	result := parsedHookOutput{Continue: true, RawExitCode: exitCode}

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
	if b, ok := parsed["continue"].(bool); ok {
		result.Continue = b
	}
	if b, ok := parsed["suppressOutput"].(bool); ok {
		result.SuppressOutput = b
	}
	if s, ok := parsed["systemMessage"].(string); ok {
		result.SystemMessage = s
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

	// Stop hooks: "block" decision prevents the session from ending.
	if parsed["decision"] == "block" {
		result.Blocked = true
		if reason, ok := parsed["reason"].(string); ok {
			result.BlockReason = reason
		}
	}

	return result
}

// runHook executes a single hook (command or prompt) and returns the parsed output.
func (r *hookRunner) runHook(ctx context.Context, hook RegisteredHook, input hookInput) parsedHookOutput {
	var hr hookResult
	var err error

	switch hook.Type {
	case "command":
		hr, err = executeCommandHook(ctx, hook, input)
	case "prompt":
		if r.client == nil {
			return parsedHookOutput{Continue: true, SystemMessage: "prompt hook skipped: no LLM client"}
		}
		hr, err = executePromptHook(ctx, r.client, r.model, hook, input)
	default:
		return parsedHookOutput{Continue: true}
	}

	if err != nil {
		return parsedHookOutput{Continue: true, IsError: true, SystemMessage: err.Error()}
	}

	// Per Claude Code spec: exit code 2 means stderr is fed back to Claude.
	output := hr.Stdout
	if hr.ExitCode == 2 && strings.TrimSpace(hr.Stderr) != "" {
		output = hr.Stderr
	}
	return parseHookOutput(output, hr.ExitCode)
}

// runAll executes all matched hooks in parallel and returns their parsed outputs.
func (r *hookRunner) runAll(ctx context.Context, event HookEvent, toolName string, input hookInput) []parsedHookOutput {
	claudeName := mapSerfToolNameToClaude(toolName)
	matched := r.matchHooks(event, claudeName)
	if len(matched) == 0 {
		return nil
	}

	results := make([]parsedHookOutput, len(matched))
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
func (r *hookRunner) RunPreToolUse(ctx context.Context, input hookInput) preToolUseResult {
	outputs := r.runAll(ctx, HookPreToolUse, input.ToolName, input)
	var result preToolUseResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.Denied || o.RawExitCode == 2 {
			result.Denied = true
			if result.DenyMessage == "" {
				result.DenyMessage = o.SystemMessage
			}
		}
		if o.UpdatedInput != nil {
			result.UpdatedInput = mergeHookInputMaps(result.UpdatedInput, o.UpdatedInput)
		}
	}
	return result
}

func mergeHookInputMaps(dst map[string]any, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// RunPostToolUse dispatches PostToolUse hooks and aggregates system messages.
func (r *hookRunner) RunPostToolUse(ctx context.Context, input hookInput) hookRunResult {
	outputs := r.runAll(ctx, HookPostToolUse, input.ToolName, input)
	return collectSystemMessages(outputs)
}

// RunStop dispatches Stop hooks and aggregates results.
// Any block from any hook means blocked.
func (r *hookRunner) RunStop(ctx context.Context, input hookInput) stopResult {
	return r.runStopEvent(ctx, HookStop, input)
}

// RunSubagentStop dispatches SubagentStop hooks and aggregates results.
func (r *hookRunner) RunSubagentStop(ctx context.Context, input hookInput) stopResult {
	return r.runStopEvent(ctx, HookSubagentStop, input)
}

func (r *hookRunner) runStopEvent(ctx context.Context, event HookEvent, input hookInput) stopResult {
	outputs := r.runAll(ctx, event, input.ToolName, input)
	var result stopResult
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
func (r *hookRunner) RunUserPromptSubmit(ctx context.Context, input hookInput) hookRunResult {
	outputs := r.runAll(ctx, HookUserPromptSubmit, "", input)
	return collectSystemMessages(outputs)
}

// RunSessionStart dispatches startup SessionStart hooks.
func (r *hookRunner) RunSessionStart(ctx context.Context, input hookInput) hookRunResult {
	return r.RunSessionStartFor(ctx, input, SessionStartKindStartup)
}

// RunSessionStartFor dispatches SessionStart hooks for a specific startup kind.
func (r *hookRunner) RunSessionStartFor(ctx context.Context, input hookInput, kind SessionStartKind) hookRunResult {
	target := strings.TrimSpace(string(kind))
	if target == "" {
		target = string(SessionStartKindStartup)
	}
	outputs := r.runAll(ctx, HookSessionStart, target, input)
	return collectSystemMessages(outputs)
}

// RunSessionEnd dispatches SessionEnd hooks. No return value needed.
func (r *hookRunner) RunSessionEnd(ctx context.Context, input hookInput) {
	r.runAll(ctx, HookSessionEnd, "", input)
}

// RunPreCompact dispatches PreCompact hooks.
func (r *hookRunner) RunPreCompact(ctx context.Context, input hookInput) hookRunResult {
	outputs := r.runAll(ctx, HookPreCompact, "", input)
	return collectSystemMessages(outputs)
}

// RunNotification dispatches Notification hooks.
func (r *hookRunner) RunNotification(ctx context.Context, input hookInput) hookRunResult {
	outputs := r.runAll(ctx, HookNotification, "", input)
	return collectSystemMessages(outputs)
}

// collectSystemMessages aggregates system messages from parsed outputs.
func collectSystemMessages(outputs []parsedHookOutput) hookRunResult {
	var result hookRunResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
	}
	return result
}
