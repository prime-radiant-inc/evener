package plugin

import (
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

	hooks, err := parsePluginHooks(data, "/plugins/test", "test-plugin")
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

	hooks, err := parsePluginHooks(data, "/plugins/test", "test-plugin")
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

	hooks, err := parsePluginHooks(data, "/my/plugin", "test-plugin")
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

	hooks, err := parsePluginHooks(data, "/plugins/test", "test-plugin")
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

	hooks, err := parsePluginHooks(data, "/plugins/test", "test-plugin")
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
	evs := []HookEvent{
		HookPreToolUse, HookPostToolUse, HookStop, HookSubagentStop,
		HookUserPromptSubmit, HookSessionStart, HookSessionEnd,
		HookPreCompact, HookNotification,
	}

	// Build JSON with all event types
	inner := ""
	var innerSb186 strings.Builder
	for i, e := range evs {
		if i > 0 {
			innerSb186.WriteString(",")
		}
		innerSb186.WriteString(`"` + string(e) + `": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo ` + string(e) + `"}]}]`)
	}
	inner += innerSb186.String()
	data := []byte(`{` + inner + `}`)

	hooks, err := parsePluginHooks(data, "/plugins/test", "test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range evs {
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

// --- Phase 1: Characterization tests (lock current parser behavior) ---

// TestParsePluginHooks_FieldsNowCaptured documents that args/shell and other Claude
// handler fields are captured by the parser (Task 2 behavior flip from the original
// characterization test that asserted they were dropped).
func TestParsePluginHooks_FieldsNowCaptured(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo x","args":["a"],"shell":"bash"}]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if h.Command != "echo x" {
		t.Fatalf("command parse drifted: %+v", h)
	}
	// Task 2: args and shell are now captured.
	if len(h.Args) != 1 || h.Args[0] != "a" {
		t.Fatalf("args not captured: %+v", h)
	}
	if h.Shell != "bash" {
		t.Fatalf("shell not captured: %+v", h)
	}
}

func TestParsePluginHooks_CapturesArgsShellAndMetadata(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"Bash","hooks":[
		{"type":"command","command":"x","args":["-c","y"],"shell":"bash","if":"Bash(rm *)","statusMessage":"checking","async":true,"asyncRewake":true}
	]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if len(h.Args) != 2 || h.Args[0] != "-c" {
		t.Fatalf("args: %+v", h)
	}
	if h.Shell != "bash" || h.If != "Bash(rm *)" || h.StatusMessage != "checking" {
		t.Fatalf("fields: %+v", h)
	}
	if !h.Async || !h.AsyncRewake {
		t.Fatalf("async flags: %+v", h)
	}
	if h.Event != HookPreToolUse || h.GroupIndex != 0 || h.HandlerIndex != 0 {
		t.Fatalf("metadata: %+v", h)
	}
}

// TestParsePluginHooks_UnknownFieldsCapturedFromRaw verifies that unrecognized
// handler keys are recovered from the RAW handler JSON. encoding/json drops
// unknown keys when decoding into hookSpec, so the capture must read the original
// bytes, not a re-marshaled hookSpec (Fix 1).
func TestParsePluginHooks_UnknownFieldsCapturedFromRaw(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"*","hooks":[
		{"type":"command","command":"x","futureField":1,"url":"http://example","headers":{"k":"v"}}
	]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	want := map[string]bool{"futureField": true, "url": true, "headers": true}
	if len(h.UnknownFields) != len(want) {
		t.Fatalf("UnknownFields = %v, want exactly keys %v", h.UnknownFields, want)
	}
	for k := range want {
		if _, ok := h.UnknownFields[k]; !ok {
			t.Errorf("UnknownFields missing key %q; got %v", k, h.UnknownFields)
		}
	}
}

// TestParsePluginHooks_NoUnknownFieldsWhenClean verifies a handler using only
// known fields produces an empty UnknownFields (Fix 1).
func TestParsePluginHooks_NoUnknownFieldsWhenClean(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"*","hooks":[
		{"type":"command","command":"x","args":["-c","y"],"shell":"bash","timeout":10}
	]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if len(h.UnknownFields) != 0 {
		t.Fatalf("clean handler UnknownFields = %v, want empty", h.UnknownFields)
	}
}

// TestParsePluginHooks_UnknownFieldsCapturedFromFile verifies that the raw-bytes
// capture path also works when hooks are loaded from a wrapper-format file (the
// most common real-world layout), not just inline direct format (Fix 1).
func TestParsePluginHooks_UnknownFieldsWrapperFormat(t *testing.T) {
	data := []byte(`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[
		{"type":"command","command":"x","futureField":1}
	]}]}}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if _, ok := h.UnknownFields["futureField"]; !ok {
		t.Fatalf("wrapper-format UnknownFields missing futureField; got %v", h.UnknownFields)
	}
}

// TestParsePluginHooks_ExpandsPluginRootInArgs verifies that ${CLAUDE_PLUGIN_ROOT}
// and ${PLUGIN_ROOT} are expanded in each Args element, matching the Command/Prompt
// substitution. Exec form has no shell, so without this the placeholder would reach
// the program literally despite the docs promising expansion (Fix 2).
func TestParsePluginHooks_ExpandsPluginRootInArgs(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"*","hooks":[
		{"type":"command","command":"check","args":["--data","${CLAUDE_PLUGIN_ROOT}/d","--alt","${PLUGIN_ROOT}/e"]}
	]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if len(h.Args) != 4 {
		t.Fatalf("args length = %d, want 4: %v", len(h.Args), h.Args)
	}
	if h.Args[1] != "/p/d" {
		t.Errorf("Args[1] = %q, want %q", h.Args[1], "/p/d")
	}
	if h.Args[3] != "/p/e" {
		t.Errorf("Args[3] = %q, want %q", h.Args[3], "/p/e")
	}
	// Non-placeholder args pass through unchanged.
	if h.Args[0] != "--data" || h.Args[2] != "--alt" {
		t.Errorf("literal args mangled: %v", h.Args)
	}
}

// TestParsePluginHooks_DirectFormatIgnoresSchemaMeta verifies that a direct-format
// file with a "$schema" meta key (and other $-prefixed keys) is not classified as
// an unknown event, while real events still parse (Fix 6).
func TestParsePluginHooks_DirectFormatIgnoresSchemaMeta(t *testing.T) {
	data := []byte(`{
		"$schema": "https://example.com/hooks.schema.json",
		"$comment": "ignore me",
		"description": "my hooks",
		"PreToolUse": [{"matcher":"*","hooks":[{"type":"command","command":"x"}]}]
	}`)
	hooks, unsupported, unknown, err := parsePluginHooksDiag(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("meta keys must not be unknown events; got unknown=%v", unknown)
	}
	if len(unsupported) != 0 {
		t.Fatalf("unexpected unsupported=%v", unsupported)
	}
	if len(hooks[HookPreToolUse]) != 1 {
		t.Fatalf("real event did not parse: %v", hooks)
	}
}

func TestParsePluginHooks_RecognizedButUnsupportedEvent(t *testing.T) {
	data := []byte(`{"PostToolUseFailure":[{"matcher":"*","hooks":[{"type":"command","command":"x"}]}],
		"TotallyBogusEvent":[{"hooks":[{"type":"command","command":"y"}]}]}`)
	hooks, unsupported, unknown, err := parsePluginHooksDiag(data, "/p", "n")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 0 {
		t.Fatalf("no supported events expected: %v", hooks)
	}
	if !unsupported[HookEvent("PostToolUseFailure")] {
		t.Fatal("PostToolUseFailure should be recognized-but-unsupported")
	}
	if !unknown["TotallyBogusEvent"] {
		t.Fatal("bogus event should be unknown")
	}
}
