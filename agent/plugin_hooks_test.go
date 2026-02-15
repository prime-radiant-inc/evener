package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Command:   "echo PLUGIN=$CLAUDE_PLUGIN_ROOT PROJECT=$CLAUDE_PROJECT_DIR",
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
	if !strings.Contains(result.Stdout, "PLUGIN=/my/plugin/dir") {
		t.Errorf("stdout should contain PLUGIN=/my/plugin/dir, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PROJECT=/my/project") {
		t.Errorf("stdout should contain PROJECT=/my/project, got %q", result.Stdout)
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
