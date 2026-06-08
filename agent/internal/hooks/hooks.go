package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/toolname"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// Input is the JSON payload piped to command hooks via stdin.
type Input struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	Message       string         `json:"message,omitempty"`
	Reason        string         `json:"reason,omitempty"`

	// Official Claude-compatible fields (claude-compatible-subset).
	// These are additive; hooks may read them when present.
	TranscriptPath string `json:"transcript_path,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
	ToolResponse   string `json:"tool_response,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
	// Effort is the reasoning effort level for the current session (e.g. "low", "medium", "high").
	// Used by Task 7 to set the CLAUDE_EFFORT env var for command hooks.
	Effort string `json:"effort,omitempty"`

	// Legacy Serf aliases retained during migration.
	// tool_result = tool_response; user_prompt = prompt.
	ToolResult string `json:"tool_result,omitempty"`
	UserPrompt string `json:"user_prompt,omitempty"`
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
//
// Exec form: when hook.Args is non-empty, the command is spawned directly without
// shell interpretation (hook.Shell is ignored). Shell form: when hook.Args is empty,
// the command is run via the selected shell — "" or "bash" → bash -c <command>;
// "powershell" → reserved, returns an error; any other value → error.
func executeCommandHook(ctx context.Context, hook plugin.RegisteredHook, input Input) (hookResult, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return hookResult{}, fmt.Errorf("marshaling hook input: %w", err)
	}

	timeout := time.Duration(hook.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if len(hook.Args) > 0 {
		// Exec form: direct spawn, no shell interpretation.
		cmd = exec.CommandContext(ctx, hook.Command, hook.Args...)
	} else {
		// Shell form: select shell.
		switch hook.Shell {
		case "", "bash":
			cmd = exec.CommandContext(ctx, "bash", "-c", hook.Command)
		case "powershell":
			return hookResult{}, errors.New("powershell shell not supported on this platform")
		default:
			return hookResult{}, fmt.Errorf("unsupported shell %q: only \"bash\" is supported", hook.Shell)
		}
	}

	cmd.Stdin = bytes.NewReader(inputJSON)
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+hook.PluginDir,
		"PLUGIN_ROOT="+hook.PluginDir,
		"CLAUDE_PROJECT_DIR="+input.CWD,
	)
	// CLAUDE_EFFORT: set only when the session has a configured effort level.
	// CLAUDE_CODE_REMOTE is intentionally not set here: serf has no remote/serve
	// signal reachable at the hook exec site; fabricating a value is forbidden by
	// the diagnostics spec (07 §"Common environment variables for command hooks").
	if input.Effort != "" {
		env = append(env, "CLAUDE_EFFORT="+input.Effort)
	}
	cmd.Env = env

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
func substituteHookVariables(prompt string, input Input) string {
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
func executePromptHook(ctx context.Context, client promptHookClient, model string, hook plugin.RegisteredHook, input Input) (hookResult, error) {
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

// Runner orchestrates hook matching and parallel dispatch.
type Runner struct {
	hooks   map[plugin.HookEvent][]plugin.RegisteredHook
	client  promptHookClient
	model   string
	onEvent func(events.EventKind, events.EventData) // optional event callback
}

// SetEventCallback sets an optional callback that is invoked for hook
// lifecycle events (HookStart, HookEnd).
func (r *Runner) SetEventCallback(fn func(events.EventKind, events.EventData)) {
	r.onEvent = fn
}

// NewRunner creates a Runner that dispatches plugin hooks. Prompt hooks call
// client with the given default model; pass a nil client to disable prompt
// hooks (command hooks still run).
func NewRunner(client *llm.Client, model string) *Runner {
	var c promptHookClient
	if client != nil {
		c = clientAdapter{client}
	}
	return newRunner(c, model)
}

// newRunner creates a Runner with the given prompt-hook client and default
// model for prompt hooks.
func newRunner(client promptHookClient, model string) *Runner {
	return &Runner{
		hooks:  make(map[plugin.HookEvent][]plugin.RegisteredHook),
		client: client,
		model:  model,
	}
}

// Summary returns the number of registered hooks per event.
// Events with zero hooks are omitted.
func (r *Runner) Summary() map[plugin.HookEvent]int {
	out := make(map[plugin.HookEvent]int)
	for event, hooks := range r.hooks {
		if len(hooks) > 0 {
			out[event] = len(hooks)
		}
	}
	return out
}

// Add registers hooks for an event.
func (r *Runner) Add(event plugin.HookEvent, hooks ...plugin.RegisteredHook) {
	r.hooks[event] = append(r.hooks[event], hooks...)
}

// MatchHooks returns hooks registered for the event whose Matcher matches toolName.
// Matching follows Claude-compatible semantics: empty/"*" match all; a matcher
// of only [A-Za-z0-9_|] chars is treated as exact or pipe-list; anything else
// is a Go RE2 regex. Invalid regex matchers are skipped without panicking and
// a sanitized diagnostic is emitted when an event callback is registered.
func (r *Runner) MatchHooks(event plugin.HookEvent, toolName string) []plugin.RegisteredHook {
	var matched []plugin.RegisteredHook
	for _, hook := range r.hooks[event] {
		ok, err := matchTarget(hook.Matcher, toolName)
		if err != nil {
			// Invalid regex: skip the hook and surface a sanitized diagnostic.
			if r.onEvent != nil {
				r.onEvent(events.EventHookEnd, events.HookEndData{
					Event:      string(event),
					HookType:   hook.Type,
					Matcher:    hook.Matcher,
					PluginName: hook.PluginName,
					ExitCode:   -1,
					// TODO(task7): surface invalid_matcher as a dedicated diagnostic category.
				})
			}
			continue
		}
		if ok {
			matched = append(matched, hook)
		}
	}
	return matched
}

// RunResult contains the aggregated output from running hooks.
type RunResult struct {
	SystemMessages    []string
	AdditionalContext []string
	TerminalSequences []string
}

// PreToolUseResult contains aggregated output from PreToolUse hooks.
type PreToolUseResult struct {
	Denied            bool
	DenyMessage       string
	SystemMessages    []string
	AdditionalContext []string
	TerminalSequences []string
	UpdatedInput      map[string]any
}

// StopResult contains aggregated output from Stop/SubagentStop hooks.
type StopResult struct {
	Blocked           bool
	BlockReason       string
	SystemMessages    []string
	AdditionalContext []string
	TerminalSequences []string
}

// parsedHookOutput is the structured interpretation of a hook's stdout and exit code.
type parsedHookOutput struct {
	Continue          bool
	SuppressOutput    bool
	SystemMessage     string
	AdditionalContext string
	TerminalSequence  string
	Denied            bool
	UpdatedInput      map[string]any
	Blocked           bool
	BlockReason       string
	IsError           bool
	RawExitCode       int
}

// parseHookOutput interprets hook stdout and exit code into structured output.
// For command hooks, JSON is parsed only when exitCode == 0; on exit 2 the
// caller should pass the stderr content as stdout and JSON is not attempted.
// This function is event-agnostic: blocking decisions are made by the callers
// that consult exitBehavior(event).BlockOnExit2.
func parseHookOutput(stdout string, exitCode int) parsedHookOutput {
	result := parsedHookOutput{Continue: true, RawExitCode: exitCode}

	if exitCode != 0 {
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
	if s, ok := parsed["terminalSequence"].(string); ok {
		result.TerminalSequence = s
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
		// additionalContext is model context; route separately from user-visible systemMessage.
		if ac, ok := hso["additionalContext"].(string); ok {
			result.AdditionalContext = ac
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
// For command hooks, JSON is parsed only on exit 0; on exit 2 the stderr path is used
// and JSON is ignored per the Claude spec (07 §Exit-code semantics).
// The event is used to determine whether exit 2 blocks the action via exitBehavior.
func (r *Runner) runHook(ctx context.Context, hook plugin.RegisteredHook, event plugin.HookEvent, input Input) parsedHookOutput {
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

	// Per Claude Code spec (07 §"General parsing rules"): for command hooks, JSON is
	// parsed only on exit 0. On exit 2, stderr is fed back as the error message and
	// JSON is ignored. Other non-zero exit codes are also non-blocking errors.
	output := hr.Stdout
	if hr.ExitCode != 0 && strings.TrimSpace(hr.Stderr) != "" {
		output = hr.Stderr
	}
	return parseHookOutput(output, hr.ExitCode)
}

// runAll executes all matched hooks in parallel and returns their parsed outputs.
func (r *Runner) runAll(ctx context.Context, event plugin.HookEvent, toolName string, input Input) []parsedHookOutput {
	claudeName := toolname.SerfToClaude(toolName)
	matched := r.MatchHooks(event, claudeName)
	if len(matched) == 0 {
		return nil
	}

	results := make([]parsedHookOutput, len(matched))
	var wg sync.WaitGroup
	wg.Add(len(matched))

	for i, hook := range matched {
		go func(idx int, h plugin.RegisteredHook) {
			defer wg.Done()
			if r.onEvent != nil {
				r.onEvent(events.EventHookStart, events.HookStartData{
					Event:      string(event),
					HookType:   h.Type,
					Matcher:    h.Matcher,
					PluginName: h.PluginName,
				})
			}
			start := time.Now()
			results[idx] = r.runHook(ctx, h, event, input)
			elapsed := time.Since(start)
			if r.onEvent != nil {
				r.onEvent(events.EventHookEnd, events.HookEndData{
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
func (r *Runner) RunPreToolUse(ctx context.Context, input Input) PreToolUseResult {
	outputs := r.runAll(ctx, plugin.HookPreToolUse, input.ToolName, input)
	var result PreToolUseResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
		if o.TerminalSequence != "" {
			result.TerminalSequences = append(result.TerminalSequences, o.TerminalSequence)
		}
		if o.Denied || (exitBehavior(plugin.HookPreToolUse).BlockOnExit2 && o.RawExitCode == 2) {
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
func (r *Runner) RunPostToolUse(ctx context.Context, input Input) RunResult {
	outputs := r.runAll(ctx, plugin.HookPostToolUse, input.ToolName, input)
	return collectSystemMessages(outputs)
}

// RunStop dispatches Stop hooks and aggregates results.
// Any block from any hook means blocked.
func (r *Runner) RunStop(ctx context.Context, input Input) StopResult {
	return r.runStopEvent(ctx, plugin.HookStop, input)
}

// RunSubagentStop dispatches SubagentStop hooks and aggregates results.
func (r *Runner) RunSubagentStop(ctx context.Context, input Input) StopResult {
	return r.runStopEvent(ctx, plugin.HookSubagentStop, input)
}

func (r *Runner) runStopEvent(ctx context.Context, event plugin.HookEvent, input Input) StopResult {
	outputs := r.runAll(ctx, event, input.ToolName, input)
	var result StopResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
		if o.TerminalSequence != "" {
			result.TerminalSequences = append(result.TerminalSequences, o.TerminalSequence)
		}
		if o.Blocked || (exitBehavior(event).BlockOnExit2 && o.RawExitCode == 2) {
			result.Blocked = true
			if result.BlockReason == "" {
				result.BlockReason = o.BlockReason
			}
		}
	}
	return result
}

// RunUserPromptSubmit dispatches UserPromptSubmit hooks.
func (r *Runner) RunUserPromptSubmit(ctx context.Context, input Input) RunResult {
	outputs := r.runAll(ctx, plugin.HookUserPromptSubmit, "", input)
	return collectSystemMessages(outputs)
}

// RunSessionStart dispatches startup SessionStart hooks.
func (r *Runner) RunSessionStart(ctx context.Context, input Input) RunResult {
	return r.RunSessionStartFor(ctx, input, plugin.SessionStartKindStartup)
}

// RunSessionStartFor dispatches SessionStart hooks for a specific startup kind.
func (r *Runner) RunSessionStartFor(ctx context.Context, input Input, kind plugin.SessionStartKind) RunResult {
	target := strings.TrimSpace(string(kind))
	if target == "" {
		target = string(plugin.SessionStartKindStartup)
	}
	outputs := r.runAll(ctx, plugin.HookSessionStart, target, input)
	return collectSystemMessages(outputs)
}

// RunSessionEnd dispatches SessionEnd hooks. No return value needed.
func (r *Runner) RunSessionEnd(ctx context.Context, input Input) {
	r.runAll(ctx, plugin.HookSessionEnd, "", input)
}

// RunPreCompact dispatches PreCompact hooks.
func (r *Runner) RunPreCompact(ctx context.Context, input Input) RunResult {
	outputs := r.runAll(ctx, plugin.HookPreCompact, "", input)
	return collectSystemMessages(outputs)
}

// RunNotification dispatches Notification hooks.
func (r *Runner) RunNotification(ctx context.Context, input Input) RunResult {
	outputs := r.runAll(ctx, plugin.HookNotification, "", input)
	return collectSystemMessages(outputs)
}

// collectSystemMessages aggregates system messages, additional context, and terminal
// sequences from parsed outputs.
func collectSystemMessages(outputs []parsedHookOutput) RunResult {
	var result RunResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
		if o.TerminalSequence != "" {
			result.TerminalSequences = append(result.TerminalSequences, o.TerminalSequence)
		}
	}
	return result
}
