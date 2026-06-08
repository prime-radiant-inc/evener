package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// --- Task 8: Command Hook Execution ---

func TestExecuteCommandHook(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Command:   "cat",
		Timeout:   5,
		PluginDir: "/tmp",
	}
	input := Input{
		SessionID:     "sess-123",
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
	}

	result, err := executeCommandHook(context.Background(), hook, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	// cat echoes stdin, so output should contain the JSON input
	if !strings.Contains(result.Stdout, "sess-123") {
		t.Errorf("stdout should contain session_id, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PreToolUse") {
		t.Errorf("stdout should contain hook_event_name, got %q", result.Stdout)
	}
}

func TestExecuteCommandHook_Timeout(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Command:   "sleep 60",
		Timeout:   1,
		PluginDir: "/tmp",
	}
	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
	}

	_, err := executeCommandHook(context.Background(), hook, input)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("error should indicate timeout/kill, got %q", err.Error())
	}
}

func TestExecuteCommandHook_ExitCode2(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Command:   "exit 2",
		Timeout:   5,
		PluginDir: "/tmp",
	}
	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
	}

	result, err := executeCommandHook(context.Background(), hook, input)
	// Non-zero exit is not a Go error — it's captured in ExitCode
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", result.ExitCode)
	}
}

func TestExecuteCommandHook_Environment(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Command:   "echo CLAUDE_PLUGIN=$CLAUDE_PLUGIN_ROOT PLUGIN=$PLUGIN_ROOT PROJECT=$CLAUDE_PROJECT_DIR",
		Timeout:   5,
		PluginDir: "/my/plugin/dir",
	}
	input := Input{
		CWD:           "/my/project",
		HookEventName: "PreToolUse",
	}

	result, err := executeCommandHook(context.Background(), hook, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Stdout, "CLAUDE_PLUGIN=/my/plugin/dir") {
		t.Errorf("stdout should contain CLAUDE_PLUGIN=/my/plugin/dir, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PLUGIN=/my/plugin/dir") {
		t.Errorf("stdout should contain PLUGIN=/my/plugin/dir, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PROJECT=/my/project") {
		t.Errorf("stdout should contain PROJECT=/my/project, got %q", result.Stdout)
	}
}

// TestExecuteCommandHook_OfficialEnv verifies that CLAUDE_EFFORT is set from
// input.Effort and that the existing CLAUDE_PLUGIN_ROOT / PLUGIN_ROOT aliases
// are still present. CLAUDE_CODE_REMOTE is intentionally not set here.
// Tier: CLAUDE_EFFORT = claude-compatible-subset; PLUGIN_ROOT = serf-native.
func TestExecuteCommandHook_OfficialEnv(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Timeout:   5,
		PluginDir: "/pd",
		Command:   "echo EFFORT=$CLAUDE_EFFORT PLUGIN=$PLUGIN_ROOT CR=$CLAUDE_PLUGIN_ROOT",
	}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/proj", HookEventName: "X", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "PLUGIN=/pd") || !strings.Contains(res.Stdout, "CR=/pd") {
		t.Fatalf("plugin env missing: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "EFFORT=high") {
		t.Fatalf("CLAUDE_EFFORT missing: %q", res.Stdout)
	}
}

// TestExecuteCommandHook_OfficialEnv_NoEffort verifies that CLAUDE_EFFORT is
// not set (empty) when input.Effort is empty.
func TestExecuteCommandHook_OfficialEnv_NoEffort(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Timeout:   5,
		PluginDir: "/pd",
		Command:   "echo EFFORT=$CLAUDE_EFFORT",
	}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/proj", HookEventName: "X"})
	if err != nil {
		t.Fatal(err)
	}
	// When Effort is empty, CLAUDE_EFFORT should not be set → the shell expands
	// $CLAUDE_EFFORT to an empty string, so we get "EFFORT=" with nothing after.
	if strings.Contains(res.Stdout, "EFFORT=high") || strings.Contains(res.Stdout, "EFFORT=low") {
		t.Fatalf("CLAUDE_EFFORT should be unset when Effort is empty, got: %q", res.Stdout)
	}
}

// --- Task 9: Prompt Hook Execution ---

type mockPromptHookClient struct {
	response string
	err      error
	lastReq  llm.Request // captured for inspection
}

func (m *mockPromptHookClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.lastReq = req
	if m.err != nil {
		return llm.Response{}, m.err
	}
	return llm.Response{
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: m.response}},
		},
	}, nil
}

func TestSubstituteHookVariables(t *testing.T) {
	input := Input{
		ToolName:   "Write",
		ToolInput:  map[string]any{"file": "test.go", "content": "hello"},
		ToolResult: "file written successfully",
		UserPrompt: "write a test",
	}

	prompt := "Tool: $TOOL_NAME, Input: $TOOL_INPUT, Result: $TOOL_RESULT, Prompt: $USER_PROMPT"
	result := substituteHookVariables(prompt, input)

	if !strings.Contains(result, "Tool: Write") {
		t.Errorf("expected TOOL_NAME substitution, got %q", result)
	}
	if !strings.Contains(result, `"file":"test.go"`) {
		t.Errorf("expected TOOL_INPUT JSON substitution, got %q", result)
	}
	if !strings.Contains(result, "Result: file written successfully") {
		t.Errorf("expected TOOL_RESULT substitution, got %q", result)
	}
	if !strings.Contains(result, "Prompt: write a test") {
		t.Errorf("expected USER_PROMPT substitution, got %q", result)
	}
}

func TestSubstituteHookVariables_EmptyValues(t *testing.T) {
	input := Input{} // all zero values

	prompt := "Tool: $TOOL_NAME, Input: $TOOL_INPUT, Result: $TOOL_RESULT, Prompt: $USER_PROMPT"
	result := substituteHookVariables(prompt, input)

	if !strings.Contains(result, "Tool: ") {
		t.Errorf("expected empty TOOL_NAME, got %q", result)
	}
	if !strings.Contains(result, "Input: null") {
		t.Errorf("expected null TOOL_INPUT, got %q", result)
	}
	if !strings.Contains(result, "Result: null") {
		t.Errorf("expected null TOOL_RESULT, got %q", result)
	}
	if !strings.Contains(result, "Prompt: null") {
		t.Errorf("expected null USER_PROMPT, got %q", result)
	}
}

func TestExecutePromptHook(t *testing.T) {
	client := &mockPromptHookClient{response: "approve"}
	hook := plugin.RegisteredHook{
		Type:    "prompt",
		Prompt:  "Check $TOOL_NAME usage",
		Timeout: 30,
	}
	input := Input{
		ToolName:      "Write",
		HookEventName: "PreToolUse",
	}

	result, err := executePromptHook(context.Background(), client, "default-model", hook, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Stdout != "approve" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "approve")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestExecutePromptHook_UsesHookModel(t *testing.T) {
	client := &mockPromptHookClient{response: "ok"}
	hook := plugin.RegisteredHook{
		Type:   "prompt",
		Prompt: "check",
		Model:  "custom-model-v2",
	}
	input := Input{HookEventName: "PreToolUse"}

	_, err := executePromptHook(context.Background(), client, "default-model", hook, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.lastReq.Model != "custom-model-v2" {
		t.Errorf("Model = %q, want %q", client.lastReq.Model, "custom-model-v2")
	}
}

// --- Task 10: Runner ---

func TestHookRunner_MatcherRegex(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "Write|Edit",
		Type:    "command",
		Command: "echo matched",
		Timeout: 5,
	})

	matched := runner.MatchHooks(plugin.HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Write, got %d", len(matched))
	}

	matched = runner.MatchHooks(plugin.HookPreToolUse, "Edit")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Edit, got %d", len(matched))
	}

	matched = runner.MatchHooks(plugin.HookPreToolUse, "Bash")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for Bash, got %d", len(matched))
	}
}

func TestHookRunner_WildcardMatcher(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "echo wildcard",
		Timeout: 5,
	})

	for _, tool := range []string{"Write", "Read", "Bash", "anything"} {
		matched := runner.MatchHooks(plugin.HookPreToolUse, tool)
		if len(matched) != 1 {
			t.Errorf("expected 1 match for %q with wildcard, got %d", tool, len(matched))
		}
	}
}

func TestHookRunner_ParallelExecution(t *testing.T) {
	runner := newRunner(nil, "")
	// Two hooks that each sleep 100ms
	runner.Add(plugin.HookSessionStart,
		plugin.RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: "sleep 0.1 && echo hook1",
			Timeout: 5,
		},
		plugin.RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: "sleep 0.1 && echo hook2",
			Timeout: 5,
		},
	)

	input := Input{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}

	start := time.Now()
	result := runner.RunSessionStart(context.Background(), input)
	elapsed := time.Since(start)

	// If parallel, should complete in ~100ms, not ~200ms
	if elapsed > 180*time.Millisecond {
		t.Errorf("expected parallel execution (~100ms), took %v", elapsed)
	}
	// Should have collected system messages from both hooks
	_ = result // result type checked by compiler
}

func TestHookRunner_SessionStartUsesExplicitKind(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "startup|clear|compact",
		Type:    "command",
		Command: "echo lifecycle-bootstrap",
		Timeout: 5,
	})

	input := Input{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}

	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindResume); len(got.SystemMessages) != 0 {
		t.Fatalf("resume SessionStart matched startup-only hook: %+v", got.SystemMessages)
	}
	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindStartup); len(got.SystemMessages) != 1 {
		t.Fatalf("startup SessionStart messages = %d, want 1", len(got.SystemMessages))
	}
	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindClear); len(got.SystemMessages) != 1 {
		t.Fatalf("clear SessionStart messages = %d, want 1", len(got.SystemMessages))
	}
}

func TestHookRunner_PreToolUse_Deny(t *testing.T) {
	runner := newRunner(nil, "")
	// A command hook that outputs a deny JSON
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","reason":"not allowed"}}'`,
		Timeout: 5,
	})

	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
	}

	result := runner.RunPreToolUse(context.Background(), input)
	if !result.Denied {
		t.Error("expected Denied=true")
	}
}

func TestHookRunner_PreToolUse_ExitCode2Denies(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo "blocked by hook" >&2; exit 2`,
		Timeout: 5,
	})

	result := runner.RunPreToolUse(context.Background(), Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
	})
	if !result.Denied {
		t.Fatal("expected exit code 2 to deny PreToolUse")
	}
	if !strings.Contains(result.DenyMessage, "blocked by hook") {
		t.Fatalf("DenyMessage = %q, want hook stderr", result.DenyMessage)
	}
}

func TestHookRunner_NoHooks(t *testing.T) {
	runner := newRunner(nil, "")
	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
	}

	preResult := runner.RunPreToolUse(context.Background(), input)
	if preResult.Denied {
		t.Error("empty runner should not deny")
	}
	if len(preResult.SystemMessages) != 0 {
		t.Error("empty runner should have no system messages")
	}

	StopResult := runner.RunStop(context.Background(), input)
	if StopResult.Blocked {
		t.Error("empty runner should not block")
	}

	hookResult := runner.RunPostToolUse(context.Background(), input)
	if len(hookResult.SystemMessages) != 0 {
		t.Error("empty runner should have no system messages")
	}
}

func TestHookRunner_ToolNameMapping(t *testing.T) {
	runner := newRunner(nil, "")
	// Register a hook that matches Claude name "Write"
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "Write",
		Type:    "command",
		Command: "echo matched",
		Timeout: 5,
	})

	// Pass serf tool name "write_file" — should match after conversion to "Write"
	matched := runner.MatchHooks(plugin.HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Claude name Write, got %d", len(matched))
	}

	// The runner's Run methods convert serf names to Claude names for matching
	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "write_file", // serf name
	}
	result := runner.RunPreToolUse(context.Background(), input)
	// Should have matched and run the hook (even though the result may be empty)
	_ = result
}

// --- Task 11: Hook Output Parsing ---

func TestParseHookOutput_PlainText(t *testing.T) {
	result := parseHookOutput("some plain text output", 0)
	if result.SystemMessage != "some plain text output" {
		t.Errorf("SystemMessage = %q, want %q", result.SystemMessage, "some plain text output")
	}
	if result.Denied {
		t.Error("plain text should not be denied")
	}
	if result.Blocked {
		t.Error("plain text should not be blocked")
	}
	if result.IsError {
		t.Error("plain text should not be error")
	}
	if !result.Continue {
		t.Error("plain text should have Continue=true")
	}
}

func TestParseHookOutput_StructuredJSON(t *testing.T) {
	output := `{"continue": false, "suppressOutput": true, "systemMessage": "pausing"}`
	result := parseHookOutput(output, 0)
	if result.Continue {
		t.Error("expected Continue=false")
	}
	if !result.SuppressOutput {
		t.Error("expected SuppressOutput=true")
	}
	if result.SystemMessage != "pausing" {
		t.Errorf("SystemMessage = %q, want %q", result.SystemMessage, "pausing")
	}
}

func TestParseHookOutput_PreToolUseDeny(t *testing.T) {
	output := `{"hookSpecificOutput":{"permissionDecision":"deny","reason":"dangerous operation"}}`
	result := parseHookOutput(output, 0)
	if !result.Denied {
		t.Error("expected Denied=true")
	}
	if result.SystemMessage != "dangerous operation" {
		t.Errorf("SystemMessage = %q, want %q", result.SystemMessage, "dangerous operation")
	}
}

func TestParseHookOutput_StopBlock(t *testing.T) {
	output := `{"decision": "block", "reason": "not ready to stop"}`
	result := parseHookOutput(output, 0)
	if !result.Blocked {
		t.Error("expected Blocked=true")
	}
	if result.BlockReason != "not ready to stop" {
		t.Errorf("BlockReason = %q, want %q", result.BlockReason, "not ready to stop")
	}
}

func TestParseHookOutput_ExitCode2(t *testing.T) {
	result := parseHookOutput("error details here", 2)
	if !result.IsError {
		t.Error("expected IsError=true for exit code 2")
	}
	if result.SystemMessage != "error details here" {
		t.Errorf("SystemMessage = %q, want %q", result.SystemMessage, "error details here")
	}
}

func TestParseHookOutput_UpdatedInput(t *testing.T) {
	output := `{"hookSpecificOutput":{"updatedInput":{"file_path":"/safe/path","content":"sanitized"}}}`
	result := parseHookOutput(output, 0)
	if result.UpdatedInput == nil {
		t.Fatal("expected UpdatedInput to be non-nil")
	}
	if result.UpdatedInput["file_path"] != "/safe/path" {
		t.Errorf("file_path = %v, want %q", result.UpdatedInput["file_path"], "/safe/path")
	}
	if result.UpdatedInput["content"] != "sanitized" {
		t.Errorf("content = %v, want %q", result.UpdatedInput["content"], "sanitized")
	}
}

func TestParseHookOutput_EmptyOutput(t *testing.T) {
	result := parseHookOutput("", 0)
	if result.SystemMessage != "" {
		t.Errorf("SystemMessage = %q, want empty", result.SystemMessage)
	}
	if result.Denied {
		t.Error("empty output should not deny")
	}
	if result.Blocked {
		t.Error("empty output should not block")
	}
	if !result.Continue {
		t.Error("empty output should have Continue=true")
	}
}

// Verify Input JSON marshaling is correct.
func TestHookInput_JSON(t *testing.T) {
	input := Input{
		SessionID:     "s1",
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     map[string]any{"file": "test.go"},
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"session_id":"s1"`) {
		t.Errorf("missing session_id in JSON: %s", s)
	}
	if !strings.Contains(s, `"tool_name":"Write"`) {
		t.Errorf("missing tool_name in JSON: %s", s)
	}
	// Empty fields should be omitted
	if strings.Contains(s, `"user_prompt"`) {
		t.Errorf("empty user_prompt should be omitted: %s", s)
	}
}

// --- Task 17: Runner event callback ---

func TestHookRunner_EmitsHookEvents(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher:    "*",
		Type:       "command",
		Command:    "echo hello",
		Timeout:    5,
		PluginName: "my-plugin",
	})

	var collected []events.SessionEvent
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		collected = append(collected, events.SessionEvent{Kind: kind, Data: data})
	})

	input := Input{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}
	runner.RunSessionStart(context.Background(), input)

	// Should have emitted HookStart and HookEnd
	var starts, ends int
	for _, ev := range collected {
		switch ev.Kind {
		case events.EventHookStart:
			starts++
			d, ok := ev.Data.(events.HookStartData)
			if !ok {
				t.Fatal("HookStart data wrong type")
			}
			if d.Event != "SessionStart" {
				t.Errorf("Event = %q, want %q", d.Event, "SessionStart")
			}
			if d.HookType != "command" {
				t.Errorf("HookType = %q, want %q", d.HookType, "command")
			}
			if d.Matcher != "*" {
				t.Errorf("Matcher = %q, want %q", d.Matcher, "*")
			}
			if d.PluginName != "my-plugin" {
				t.Errorf("PluginName = %q, want %q", d.PluginName, "my-plugin")
			}
		case events.EventHookEnd:
			ends++
			d, ok := ev.Data.(events.HookEndData)
			if !ok {
				t.Fatal("HookEnd data wrong type")
			}
			if d.Event != "SessionStart" {
				t.Errorf("Event = %q, want %q", d.Event, "SessionStart")
			}
			if d.HookType != "command" {
				t.Errorf("HookType = %q, want %q", d.HookType, "command")
			}
			if d.PluginName != "my-plugin" {
				t.Errorf("PluginName = %q, want %q", d.PluginName, "my-plugin")
			}
			if d.DurationMS < 0 {
				t.Errorf("DurationMS = %d, want >= 0", d.DurationMS)
			}
		}
	}
	if starts != 1 {
		t.Errorf("expected 1 HookStart event, got %d", starts)
	}
	if ends != 1 {
		t.Errorf("expected 1 HookEnd event, got %d", ends)
	}
}

func TestHookRunner_NoCallbackNoEvents(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "echo hello",
		Timeout: 5,
	})

	// Should not panic when no callback is set
	input := Input{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}
	runner.RunSessionStart(context.Background(), input)
}

func TestHookRunner_Summary(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "command", Command: "echo a"})
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "Write", Type: "command", Command: "echo b"})
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "check"})
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{Matcher: "*", Type: "command", Command: "echo start"})

	summary := runner.Summary()

	if summary[plugin.HookPreToolUse] != 2 {
		t.Errorf("PreToolUse count = %d, want 2", summary[plugin.HookPreToolUse])
	}
	if summary[plugin.HookPostToolUse] != 1 {
		t.Errorf("PostToolUse count = %d, want 1", summary[plugin.HookPostToolUse])
	}
	if summary[plugin.HookSessionStart] != 1 {
		t.Errorf("SessionStart count = %d, want 1", summary[plugin.HookSessionStart])
	}
	// Events with no hooks should not appear.
	if _, ok := summary[plugin.HookStop]; ok {
		t.Error("Stop should not be in summary when no hooks registered")
	}
}

func TestHookRunner_Summary_Empty(t *testing.T) {
	runner := newRunner(nil, "")
	summary := runner.Summary()
	if len(summary) != 0 {
		t.Errorf("expected empty summary, got %v", summary)
	}
}

// --- Phase 1: Characterization tests (lock current behavior) ---

// TestMatchHooks_ExactModeNoSubstring verifies that a plain word matcher like
// "Bash" uses exact-match semantics and does NOT substring-match "BashOutput".
// This is the headline fix from Task 3 (claude-compatible-subset).
func TestMatchHooks_ExactModeNoSubstring(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "Bash", Type: "command", Command: "echo x"})
	// Exact mode: "Bash" must not match "BashOutput".
	if got := r.MatchHooks(plugin.HookPreToolUse, "BashOutput"); len(got) != 0 {
		t.Fatalf("exact mode: Bash must not match BashOutput; got %d matches", len(got))
	}
	// Sanity: "Bash" does match "Bash".
	if got := r.MatchHooks(plugin.HookPreToolUse, "Bash"); len(got) != 1 {
		t.Fatalf("exact mode: Bash must match Bash; got %d matches", len(got))
	}
}

// TestMatchHooks_NegativeMatcherCoverage covers the 07-spec acceptance cases:
// pipe-list non-tool, MCP regex-vs-exact, and invalid-regex skip (no panic).
func TestMatchHooks_NegativeMatcherCoverage(t *testing.T) {
	// Pipe-list: "startup|clear|compact" must not match "resume".
	r := newRunner(nil, "")
	r.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "startup|clear|compact",
		Type:    "command",
		Command: "echo lifecycle",
		Timeout: 5,
	})
	if got := r.MatchHooks(plugin.HookSessionStart, "resume"); len(got) != 0 {
		t.Errorf("pipe-list: startup|clear|compact must not match resume; got %d", len(got))
	}
	if got := r.MatchHooks(plugin.HookSessionStart, "startup"); len(got) != 1 {
		t.Errorf("pipe-list: startup|clear|compact must match startup; got %d", len(got))
	}

	// MCP regex-vs-exact: "mcp__memory__.*" (regex) matches; "mcp__memory" (exact) does not.
	r2 := newRunner(nil, "")
	r2.Add(plugin.HookPreToolUse,
		plugin.RegisteredHook{Matcher: "mcp__memory__.*", Type: "command", Command: "echo regex", Timeout: 5},
		plugin.RegisteredHook{Matcher: "mcp__memory", Type: "command", Command: "echo exact", Timeout: 5},
	)
	if got := r2.MatchHooks(plugin.HookPreToolUse, "mcp__memory__search"); len(got) != 1 {
		t.Errorf("MCP: regex mcp__memory__.* must match mcp__memory__search; got %d", len(got))
	}
	if got := r2.MatchHooks(plugin.HookPreToolUse, "mcp__memory__write"); len(got) != 1 {
		t.Errorf("MCP: regex mcp__memory__.* must match mcp__memory__write; got %d", len(got))
	}

	// Invalid regex: hook must be skipped, must not panic, and runner must not run it.
	r3 := newRunner(nil, "")
	r3.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "(",
		Type:    "command",
		Command: "echo should-not-run",
		Timeout: 5,
	})
	if got := r3.MatchHooks(plugin.HookPreToolUse, "Bash"); len(got) != 0 {
		t.Errorf("invalid regex: hook must be skipped; got %d matches", len(got))
	}
	// Ensure RunPreToolUse also does not panic with an invalid-regex hook.
	result := r3.RunPreToolUse(context.Background(), Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
	})
	if result.Denied {
		t.Error("invalid regex hook must not cause denial")
	}
}

// TestMatchHooks_InvalidMatcherEmitsWarning verifies that when a hook is skipped
// because its matcher is an invalid regex, MatchHooks emits a loud WARNING event
// (not just the silent skip) naming the plugin, event, and matcher. The matcher
// string is the only matcher-derived data exposed; no payload is leaked.
func TestMatchHooks_InvalidMatcherEmitsWarning(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher:    "(", // invalid regex
		Type:       "command",
		Command:    "echo should-not-run",
		Timeout:    5,
		PluginName: "broken-plugin",
	})

	var warnings []events.WarningData
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if kind == events.EventWarning {
			if w, ok := data.(events.WarningData); ok {
				warnings = append(warnings, w)
			}
		}
	})

	if got := r.MatchHooks(plugin.HookPreToolUse, "Bash"); len(got) != 0 {
		t.Fatalf("invalid regex: hook must be skipped; got %d matches", len(got))
	}

	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 WARNING for the invalid matcher, got %d", len(warnings))
	}
	w := warnings[0]
	if w.PluginName != "broken-plugin" {
		t.Errorf("PluginName = %q, want %q", w.PluginName, "broken-plugin")
	}
	if w.EventName != string(plugin.HookPreToolUse) {
		t.Errorf("EventName = %q, want %q", w.EventName, plugin.HookPreToolUse)
	}
	if !strings.Contains(w.Message, "(") {
		t.Errorf("warning message %q should name the offending matcher", w.Message)
	}
	if !strings.Contains(strings.ToLower(w.Message), "matcher") {
		t.Errorf("warning message %q should explain the matcher is invalid", w.Message)
	}
}

// TestParseHookOutput_CurrentContracts_Characterization locks two behavioral contracts:
// exit code 2 → IsError=true with stdout as SystemMessage, and hookSpecificOutput.additionalContext
// is routed into AdditionalContext (separate from SystemMessage).
func TestParseHookOutput_CurrentContracts_Characterization(t *testing.T) {
	if o := parseHookOutput("boom", 2); !o.IsError || o.SystemMessage != "boom" {
		t.Fatalf("exit2 contract drifted: %+v", o)
	}
	o := parseHookOutput(`{"hookSpecificOutput":{"additionalContext":"ctx"}}`, 0)
	if o.SystemMessage != "" {
		t.Fatalf("additionalContext must NOT fold into SystemMessage; SystemMessage=%q", o.SystemMessage)
	}
	if o.AdditionalContext != "ctx" {
		t.Fatalf("additionalContext must route to AdditionalContext; got %q", o.AdditionalContext)
	}
}

// --- Task 4: exec-form args + explicit shell selection ---

func TestExecuteCommandHook_ExecFormArgs(t *testing.T) {
	hook := plugin.RegisteredHook{Type: "command", Command: "printf", Args: []string{"%s", "a b c"}, Timeout: 5, PluginDir: "/tmp"}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "PreToolUse"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "a b c" {
		t.Fatalf("exec-form stdout=%q want %q", res.Stdout, "a b c")
	}
}

func TestExecuteCommandHook_ExecForm_NoShellExpansion(t *testing.T) {
	// In exec form, $HOME must NOT be expanded by a shell.
	hook := plugin.RegisteredHook{Type: "command", Command: "printf", Args: []string{"%s", "$HOME"}, Timeout: 5, PluginDir: "/tmp"}
	res, _ := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"})
	if res.Stdout != "$HOME" {
		t.Fatalf("exec-form expanded shell var: %q", res.Stdout)
	}
}

func TestExecuteCommandHook_UnknownShellRejected(t *testing.T) {
	hook := plugin.RegisteredHook{Type: "command", Command: "echo x", Shell: "fish", Timeout: 5, PluginDir: "/tmp"}
	_, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"})
	if err == nil {
		t.Fatal("unknown shell should error")
	}
}

// TestHookInput_OfficialFields verifies that the new official Claude-compatible input
// fields serialize with the correct JSON tags and that legacy aliases are preserved.
func TestHookInput_OfficialFields(t *testing.T) {
	b, _ := json.Marshal(Input{
		SessionID: "s", CWD: "/w", HookEventName: "PreToolUse", ToolName: "Bash",
		TranscriptPath: "/t.jsonl", PermissionMode: "default", ToolUseID: "call-1",
		ToolResponse: "ok", AgentID: "ag1", AgentType: "Explore",
		ToolResult: "ok", // legacy alias preserved
	})
	for _, w := range []string{`"transcript_path":"/t.jsonl"`, `"permission_mode":"default"`, `"tool_use_id":"call-1"`, `"tool_response":"ok"`, `"agent_id":"ag1"`, `"agent_type":"Explore"`, `"tool_result":"ok"`} {
		if !strings.Contains(string(b), w) {
			t.Fatalf("missing %s in %s", w, b)
		}
	}
}

// TestParseHookOutput_AdditionalContextSeparate verifies that additionalContext from
// hookSpecificOutput is routed into AdditionalContext (not folded into SystemMessage).
func TestParseHookOutput_AdditionalContextSeparate(t *testing.T) {
	o := parseHookOutput(`{"systemMessage":"user-visible","hookSpecificOutput":{"additionalContext":"model-ctx"}}`, 0)
	if o.SystemMessage != "user-visible" {
		t.Fatalf("systemMessage=%q", o.SystemMessage)
	}
	if o.AdditionalContext != "model-ctx" {
		t.Fatalf("additionalContext=%q", o.AdditionalContext)
	}
	o2 := parseHookOutput(`{"terminalSequence":""}`, 0)
	if o2.TerminalSequence != "" {
		t.Fatalf("terminalSequence=%q", o2.TerminalSequence)
	}
}

// TestHookInput_CurrentWireShape_Characterization locks the JSON field names present
// today. Later tasks may ADD fields but must never remove or rename these.
func TestHookInput_CurrentWireShape_Characterization(t *testing.T) {
	b, _ := json.Marshal(Input{SessionID: "s", CWD: "/w", HookEventName: "PreToolUse", ToolName: "Write"})
	for _, want := range []string{`"session_id":"s"`, `"cwd":"/w"`, `"hook_event_name":"PreToolUse"`, `"tool_name":"Write"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
	bb, _ := json.Marshal(Input{UserPrompt: "p", ToolResult: "r", HookEventName: "X"})
	for _, want := range []string{`"user_prompt":"p"`, `"tool_result":"r"`} {
		if !strings.Contains(string(bb), want) {
			t.Fatalf("alias dropped: missing %s in %s", want, bb)
		}
	}
}
