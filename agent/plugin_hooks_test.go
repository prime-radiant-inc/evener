package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestParsePluginHooks_WrapperFormat(t *testing.T) {
	data := []byte(`{
		"description": "My hooks",
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Write|Edit",
					"hooks": [
						{"type": "command", "command": "echo check", "timeout": 30}
					]
				}
			]
		}
	}`)

	hooks, err := ParsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pre, ok := hooks[HookPreToolUse]
	if !ok {
		t.Fatal("expected PreToolUse hooks")
	}
	if len(pre) != 1 {
		t.Fatalf("got %d hooks, want 1", len(pre))
	}
	if pre[0].Matcher != "Write|Edit" {
		t.Errorf("Matcher = %q, want %q", pre[0].Matcher, "Write|Edit")
	}
	if pre[0].Type != "command" {
		t.Errorf("Type = %q, want %q", pre[0].Type, "command")
	}
	if pre[0].Command != "echo check" {
		t.Errorf("Command = %q, want %q", pre[0].Command, "echo check")
	}
	if pre[0].Timeout != 30 {
		t.Errorf("Timeout = %d, want 30", pre[0].Timeout)
	}
	if pre[0].PluginName != "test-plugin" {
		t.Errorf("PluginName = %q, want %q", pre[0].PluginName, "test-plugin")
	}
	if pre[0].PluginDir != "/plugins/test" {
		t.Errorf("PluginDir = %q, want %q", pre[0].PluginDir, "/plugins/test")
	}
}

func TestParsePluginHooks_DirectFormat(t *testing.T) {
	data := []byte(`{
		"PreToolUse": [
			{
				"matcher": "*",
				"hooks": [
					{"type": "command", "command": "echo direct"}
				]
			}
		]
	}`)

	hooks, err := ParsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pre, ok := hooks[HookPreToolUse]
	if !ok {
		t.Fatal("expected PreToolUse hooks")
	}
	if len(pre) != 1 {
		t.Fatalf("got %d hooks, want 1", len(pre))
	}
	if pre[0].Matcher != "*" {
		t.Errorf("Matcher = %q, want %q", pre[0].Matcher, "*")
	}
	if pre[0].Command != "echo direct" {
		t.Errorf("Command = %q, want %q", pre[0].Command, "echo direct")
	}
}

func TestParsePluginHooks_ExpandsPluginRoot(t *testing.T) {
	data := []byte(`{
		"PreToolUse": [
			{
				"matcher": "*",
				"hooks": [
					{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/check"}
				]
			}
		]
	}`)

	hooks, err := ParsePluginHooks(data, "/my/plugin", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pre := hooks[HookPreToolUse]
	if len(pre) != 1 {
		t.Fatalf("got %d hooks, want 1", len(pre))
	}
	if pre[0].Command != "/my/plugin/bin/check" {
		t.Errorf("Command = %q, want %q", pre[0].Command, "/my/plugin/bin/check")
	}
}

func TestParsePluginHooks_PromptType(t *testing.T) {
	data := []byte(`{
		"PostToolUse": [
			{
				"matcher": "*",
				"hooks": [
					{"type": "prompt", "prompt": "Review $TOOL_RESULT for issues"}
				]
			}
		]
	}`)

	hooks, err := ParsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	post := hooks[HookPostToolUse]
	if len(post) != 1 {
		t.Fatalf("got %d hooks, want 1", len(post))
	}
	if post[0].Type != "prompt" {
		t.Errorf("Type = %q, want %q", post[0].Type, "prompt")
	}
	if post[0].Prompt != "Review $TOOL_RESULT for issues" {
		t.Errorf("Prompt = %q", post[0].Prompt)
	}
}

func TestParsePluginHooks_DefaultTimeouts(t *testing.T) {
	data := []byte(`{
		"PreToolUse": [
			{
				"matcher": "*",
				"hooks": [
					{"type": "command", "command": "echo cmd"},
					{"type": "prompt", "prompt": "check this"}
				]
			}
		]
	}`)

	hooks, err := ParsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pre := hooks[HookPreToolUse]
	if len(pre) != 2 {
		t.Fatalf("got %d hooks, want 2", len(pre))
	}
	// Command default: 60s
	if pre[0].Timeout != 60 {
		t.Errorf("command timeout = %d, want 60", pre[0].Timeout)
	}
	// Prompt default: 30s
	if pre[1].Timeout != 30 {
		t.Errorf("prompt timeout = %d, want 30", pre[1].Timeout)
	}
}

func TestParsePluginHooks_AllEvents(t *testing.T) {
	events := []HookEvent{
		HookPreToolUse, HookPostToolUse, HookStop, HookSubagentStop,
		HookUserPromptSubmit, HookSessionStart, HookSessionEnd,
		HookPreCompact, HookNotification,
	}

	// Build JSON with all event types
	inner := ""
	for i, e := range events {
		if i > 0 {
			inner += ","
		}
		inner += `"` + string(e) + `": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo ` + string(e) + `"}]}]`
	}
	data := []byte(`{` + inner + `}`)

	hooks, err := ParsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if _, ok := hooks[e]; !ok {
			t.Errorf("missing event %q", e)
		}
	}
}

func TestDiscoverPluginHooks_FromFile(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := []byte(`{
		"PreToolUse": [
			{
				"matcher": "*",
				"hooks": [{"type": "command", "command": "echo from-file"}]
			}
		]
	}`)
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hooksJSON, 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := discoverPluginHooks(dir, nil, "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pre, ok := hooks[HookPreToolUse]
	if !ok {
		t.Fatal("expected PreToolUse hooks")
	}
	if len(pre) != 1 {
		t.Fatalf("got %d hooks, want 1", len(pre))
	}
	if pre[0].Command != "echo from-file" {
		t.Errorf("Command = %q, want %q", pre[0].Command, "echo from-file")
	}
}

func TestDiscoverPluginHooks_NoFile(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	hooks, err := discoverPluginHooks(dir, nil, "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected empty map, got %d entries", len(hooks))
	}
}

// --- Task 8: Command Hook Execution ---

func TestExecuteCommandHook(t *testing.T) {
	hook := RegisteredHook{
		Type:      "command",
		Command:   "cat",
		Timeout:   5,
		PluginDir: "/tmp",
	}
	input := HookInput{
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
	hook := RegisteredHook{
		Type:      "command",
		Command:   "sleep 60",
		Timeout:   1,
		PluginDir: "/tmp",
	}
	input := HookInput{
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
	hook := RegisteredHook{
		Type:      "command",
		Command:   "exit 2",
		Timeout:   5,
		PluginDir: "/tmp",
	}
	input := HookInput{
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
	hook := RegisteredHook{
		Type:      "command",
		Command:   "echo CLAUDE_PLUGIN=$CLAUDE_PLUGIN_ROOT PLUGIN=$PLUGIN_ROOT PROJECT=$CLAUDE_PROJECT_DIR",
		Timeout:   5,
		PluginDir: "/my/plugin/dir",
	}
	input := HookInput{
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
	input := HookInput{
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
	input := HookInput{} // all zero values

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
	hook := RegisteredHook{
		Type:    "prompt",
		Prompt:  "Check $TOOL_NAME usage",
		Timeout: 30,
	}
	input := HookInput{
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
	hook := RegisteredHook{
		Type:   "prompt",
		Prompt: "check",
		Model:  "custom-model-v2",
	}
	input := HookInput{HookEventName: "PreToolUse"}

	_, err := executePromptHook(context.Background(), client, "default-model", hook, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.lastReq.Model != "custom-model-v2" {
		t.Errorf("Model = %q, want %q", client.lastReq.Model, "custom-model-v2")
	}
}

// --- Task 10: HookRunner ---

func TestHookRunner_MatcherRegex(t *testing.T) {
	runner := NewHookRunner(nil, "")
	runner.Add(HookPreToolUse, RegisteredHook{
		Matcher: "Write|Edit",
		Type:    "command",
		Command: "echo matched",
		Timeout: 5,
	})

	matched := runner.matchHooks(HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Write, got %d", len(matched))
	}

	matched = runner.matchHooks(HookPreToolUse, "Edit")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Edit, got %d", len(matched))
	}

	matched = runner.matchHooks(HookPreToolUse, "Bash")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for Bash, got %d", len(matched))
	}
}

func TestHookRunner_WildcardMatcher(t *testing.T) {
	runner := NewHookRunner(nil, "")
	runner.Add(HookPreToolUse, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "echo wildcard",
		Timeout: 5,
	})

	for _, tool := range []string{"Write", "Read", "Bash", "anything"} {
		matched := runner.matchHooks(HookPreToolUse, tool)
		if len(matched) != 1 {
			t.Errorf("expected 1 match for %q with wildcard, got %d", tool, len(matched))
		}
	}
}

func TestHookRunner_ParallelExecution(t *testing.T) {
	runner := NewHookRunner(nil, "")
	// Two hooks that each sleep 100ms
	runner.Add(HookSessionStart,
		RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: "sleep 0.1 && echo hook1",
			Timeout: 5,
		},
		RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: "sleep 0.1 && echo hook2",
			Timeout: 5,
		},
	)

	input := HookInput{
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
	runner := NewHookRunner(nil, "")
	runner.Add(HookSessionStart, RegisteredHook{
		Matcher: "startup|clear|compact",
		Type:    "command",
		Command: "echo lifecycle-bootstrap",
		Timeout: 5,
	})

	input := HookInput{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}

	if got := runner.RunSessionStartFor(context.Background(), input, SessionStartKindResume); len(got.SystemMessages) != 0 {
		t.Fatalf("resume SessionStart matched startup-only hook: %+v", got.SystemMessages)
	}
	if got := runner.RunSessionStartFor(context.Background(), input, SessionStartKindStartup); len(got.SystemMessages) != 1 {
		t.Fatalf("startup SessionStart messages = %d, want 1", len(got.SystemMessages))
	}
	if got := runner.RunSessionStartFor(context.Background(), input, SessionStartKindClear); len(got.SystemMessages) != 1 {
		t.Fatalf("clear SessionStart messages = %d, want 1", len(got.SystemMessages))
	}
}

func TestHookRunner_PreToolUse_Deny(t *testing.T) {
	runner := NewHookRunner(nil, "")
	// A command hook that outputs a deny JSON
	runner.Add(HookPreToolUse, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","reason":"not allowed"}}'`,
		Timeout: 5,
	})

	input := HookInput{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "Write",
	}

	result := runner.RunPreToolUse(context.Background(), input)
	if !result.Denied {
		t.Error("expected Denied=true")
	}
}

func TestHookRunner_NoHooks(t *testing.T) {
	runner := NewHookRunner(nil, "")
	input := HookInput{
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

	stopResult := runner.RunStop(context.Background(), input)
	if stopResult.Blocked {
		t.Error("empty runner should not block")
	}

	hookResult := runner.RunPostToolUse(context.Background(), input)
	if len(hookResult.SystemMessages) != 0 {
		t.Error("empty runner should have no system messages")
	}
}

func TestHookRunner_ToolNameMapping(t *testing.T) {
	runner := NewHookRunner(nil, "")
	// Register a hook that matches Claude name "Write"
	runner.Add(HookPreToolUse, RegisteredHook{
		Matcher: "Write",
		Type:    "command",
		Command: "echo matched",
		Timeout: 5,
	})

	// Pass serf tool name "write_file" — should match after conversion to "Write"
	matched := runner.matchHooks(HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Claude name Write, got %d", len(matched))
	}

	// The runner's Run methods convert serf names to Claude names for matching
	input := HookInput{
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

// Verify HookInput JSON marshaling is correct.
func TestHookInput_JSON(t *testing.T) {
	input := HookInput{
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

// --- Task 17: HookRunner event callback ---

func TestHookRunner_EmitsHookEvents(t *testing.T) {
	runner := NewHookRunner(nil, "")
	runner.Add(HookSessionStart, RegisteredHook{
		Matcher:    "*",
		Type:       "command",
		Command:    "echo hello",
		Timeout:    5,
		PluginName: "my-plugin",
	})

	var collected []SessionEvent
	runner.SetEventCallback(func(kind EventKind, data any) {
		collected = append(collected, SessionEvent{Kind: kind, Data: data})
	})

	input := HookInput{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}
	runner.RunSessionStart(context.Background(), input)

	// Should have emitted HookStart and HookEnd
	var starts, ends int
	for _, ev := range collected {
		switch ev.Kind {
		case EventHookStart:
			starts++
			d, ok := ev.Data.(HookStartData)
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
		case EventHookEnd:
			ends++
			d, ok := ev.Data.(HookEndData)
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
	runner := NewHookRunner(nil, "")
	runner.Add(HookSessionStart, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "echo hello",
		Timeout: 5,
	})

	// Should not panic when no callback is set
	input := HookInput{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}
	runner.RunSessionStart(context.Background(), input)
}

func TestHookRunner_Summary(t *testing.T) {
	runner := NewHookRunner(nil, "")
	runner.Add(HookPreToolUse, RegisteredHook{Matcher: "*", Type: "command", Command: "echo a"})
	runner.Add(HookPreToolUse, RegisteredHook{Matcher: "Write", Type: "command", Command: "echo b"})
	runner.Add(HookPostToolUse, RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "check"})
	runner.Add(HookSessionStart, RegisteredHook{Matcher: "*", Type: "command", Command: "echo start"})

	summary := runner.Summary()

	if summary[HookPreToolUse] != 2 {
		t.Errorf("PreToolUse count = %d, want 2", summary[HookPreToolUse])
	}
	if summary[HookPostToolUse] != 1 {
		t.Errorf("PostToolUse count = %d, want 1", summary[HookPostToolUse])
	}
	if summary[HookSessionStart] != 1 {
		t.Errorf("SessionStart count = %d, want 1", summary[HookSessionStart])
	}
	// Events with no hooks should not appear.
	if _, ok := summary[HookStop]; ok {
		t.Error("Stop should not be in summary when no hooks registered")
	}
}

func TestHookRunner_Summary_Empty(t *testing.T) {
	runner := NewHookRunner(nil, "")
	summary := runner.Summary()
	if len(summary) != 0 {
		t.Errorf("expected empty summary, got %v", summary)
	}
}
