package agent

import (
	"os"
	"path/filepath"
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
