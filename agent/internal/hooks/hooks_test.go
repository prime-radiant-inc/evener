package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// TestInput_PromptFieldMarshalsAlongsideUserPrompt verifies that a
// UserPromptSubmit-style Input carries the official Claude "prompt" field as well
// as the legacy "user_prompt" alias, with the same value. A Claude-style hook
// reads "prompt"; before this fix evener sent only "user_prompt" (Fix 5).
func TestInput_PromptFieldMarshalsAlongsideUserPrompt(t *testing.T) {
	in := Input{
		HookEventName: "UserPromptSubmit",
		Prompt:        "do the thing",
		UserPrompt:    "do the thing",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["prompt"] != "do the thing" {
		t.Errorf("prompt = %v, want %q", m["prompt"], "do the thing")
	}
	if m["user_prompt"] != "do the thing" {
		t.Errorf("user_prompt = %v, want %q", m["user_prompt"], "do the thing")
	}
}

// TestRunUserPromptSubmit_PipesPromptToStdin verifies the runner pipes the
// official "prompt" field to a command hook's stdin (the end-to-end wire path),
// not only the legacy alias. A command hook echoes its stdin; the parsed output
// must contain the prompt value under "prompt" (Fix 5).
func TestRunUserPromptSubmit_PipesPromptToStdin(t *testing.T) {
	r := newRunner(nil, "")
	// The hook prints whether stdin carried a top-level "prompt" key with our value.
	r.Add(plugin.HookUserPromptSubmit, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Timeout: 5,
		Command: `grep -q '"prompt":"hello world"' && echo "prompt-present" || echo "prompt-MISSING"`,
	})

	out := r.RunUserPromptSubmit(context.Background(), Input{
		HookEventName: "UserPromptSubmit",
		Prompt:        "hello world",
		UserPrompt:    "hello world",
	})
	joined := strings.Join(out.ModelContext, " ")
	if !strings.Contains(joined, "prompt-present") {
		t.Fatalf("command hook did not see top-level \"prompt\" on stdin; messages = %v", out.ModelContext)
	}
}

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
// Tier: CLAUDE_EFFORT = claude-compatible-subset; PLUGIN_ROOT = evener-native.
func TestExecuteCommandHook_OfficialEnv(t *testing.T) {
	hook := plugin.RegisteredHook{
		Type:      "command",
		Timeout:   5,
		PluginDir: "/pd",
		Command:   "echo EFFORT=$CLAUDE_EFFORT PLUGIN=$PLUGIN_ROOT CR=$CLAUDE_PLUGIN_ROOT",
	}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/proj", HookEventName: "X", Effort: &Effort{Level: "high"}})
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
	// Simulate evener itself running under an agent that exports CLAUDE_EFFORT:
	// the parent's value must not leak into hooks when the session has none.
	t.Setenv("CLAUDE_EFFORT", "high")
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
	// The prompt hook must substitute $TOOL_NAME before sending to the LLM.
	// If substituteHookVariables is skipped, the LLM receives the raw template
	// "Check $TOOL_NAME usage" instead of the expected "Check Write usage".
	if got := client.lastReq.Messages[0].Text(); got != "Check Write usage" {
		t.Errorf("LLM received unsubstituted prompt: got %q, want %q", got, "Check Write usage")
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

	// Both hooks must have run and produced output. SessionStart is a context event,
	// so plain stdout routes to ModelContext.
	if len(result.ModelContext) != 2 {
		t.Errorf("expected output from both hooks; got ModelContext=%v", result.ModelContext)
	}
	// Serial execution would take ≥200ms; a 350ms ceiling still proves concurrency
	// while allowing headroom for slow CI shell-spawn overhead.
	if elapsed > 350*time.Millisecond {
		t.Errorf("expected parallel execution (serial would be ~200ms), took %v", elapsed)
	}
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

	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindResume); len(got.ModelContext) != 0 {
		t.Fatalf("resume SessionStart matched startup-only hook: %+v", got.ModelContext)
	}
	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindStartup); len(got.ModelContext) != 1 {
		t.Fatalf("startup SessionStart model context = %d, want 1", len(got.ModelContext))
	}
	if got := runner.RunSessionStartFor(context.Background(), input, plugin.SessionStartKindClear); len(got.ModelContext) != 1 {
		t.Fatalf("clear SessionStart model context = %d, want 1", len(got.ModelContext))
	}
}

// TestRunSubagentStartFor_MatchesAgentType proves SubagentStart hooks match on
// the child's agent type, the same value its agent_type input carries.
func TestRunSubagentStartFor_MatchesAgentType(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Matcher: "^toolsmith:toolsmith$", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"injected"}}'`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "toolsmith:toolsmith")
	if len(got.ModelContext) != 1 || got.ModelContext[0] != "injected" {
		t.Fatalf("ModelContext = %v, want the injected context", got.ModelContext)
	}
	miss := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "other")
	if len(miss.ModelContext) != 0 {
		t.Fatalf("another agent type must not match, got %v", miss.ModelContext)
	}
}

// TestRunSubagentStartFor_MatchesAnAgentTypeThatLooksLikeAToolName proves a
// non-tool matcher target is never rewritten into Claude's tool vocabulary. An
// agent type named "shell" is not a tool name: translating it would match "Bash"
// while the input's agent_type stayed "shell", so the hook aimed at the agent
// would silently never fire.
func TestRunSubagentStartFor_MatchesAnAgentTypeThatLooksLikeAToolName(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Matcher: "^shell$", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"shell injected"}}'`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart", AgentType: "shell"}, "shell")
	if len(got.ModelContext) != 1 || got.ModelContext[0] != "shell injected" {
		t.Fatalf("ModelContext = %v, want the context for an agent type named \"shell\"", got.ModelContext)
	}
	// The translated spelling must NOT be what the matcher sees, or the hook would
	// fire for a differently-named agent.
	if wrong := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart", AgentType: "Bash"}, "Bash"); len(wrong.ModelContext) != 0 {
		t.Fatalf("an agent type named \"Bash\" matched ^shell$: %v", wrong.ModelContext)
	}
}

// TestRunSubagentStartFor_Exit2IsUserOnly proves a spawn warning reaches the
// operator and never the child's model, as one summarized line.
func TestRunSubagentStartFor_Exit2IsUserOnly(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo "spawn warning" >&2; exit 2`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "subagent")
	if len(got.ModelContext) != 0 {
		t.Fatalf("exit 2 must not reach the model, got %v", got.ModelContext)
	}
	if len(got.UserMessages) != 1 {
		t.Fatalf("exit 2 stderr must reach the user, got %v", got.UserMessages)
	}
	if !strings.HasPrefix(got.UserMessages[0], "SubagentStart hook failed (exit 2)") {
		t.Fatalf("a failed SubagentStart hook must warn with the event, hook, and exit code; got %q", got.UserMessages[0])
	}

	// SubagentStart has no decision control in Claude, so decision:"block" must
	// not stop the child either: the exit-2 warning above is the only effect.
	blocked := newRunner(nil, "")
	blocked.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"decision":"block","reason":"no spawn"}'`})
	if res := blocked.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "subagent"); res.Stopped {
		t.Fatalf("SubagentStart decision:block must be dropped, got Stopped with %q", res.StopReason)
	}
}

// TestRunSubagentStartFor_HonorsContinueFalse covers Claude's universal continue
// field on the new event: the runner reports the stop and its reason, and the
// dispatch site decides how to end the launch.
func TestRunSubagentStartFor_HonorsContinueFalse(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"no spawn"}'`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "subagent")
	if !got.Stopped || got.StopReason != "no spawn" {
		t.Fatalf("Stopped/StopReason = %t/%q, want true/%q", got.Stopped, got.StopReason, "no spawn")
	}
}

func TestHookRunner_PreToolUse_Deny(t *testing.T) {
	runner := newRunner(nil, "")
	// A command hook that outputs a deny JSON
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"not allowed"}}'`,
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
	if result.DenyMessage != "not allowed" {
		t.Errorf("DenyMessage = %q, want \"not allowed\"", result.DenyMessage)
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
	// An exit-2 deny's stderr is ONLY the DenyMessage — it must not also leak into
	// the model or user buckets (guards the `!denied || !o.IsError` routing guard).
	if len(result.ModelContext) != 0 {
		t.Fatalf("exit-2 deny must not populate ModelContext: %v", result.ModelContext)
	}
	if len(result.UserMessages) != 0 {
		t.Fatalf("exit-2 deny must not populate UserMessages: %v", result.UserMessages)
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
	if len(preResult.ModelContext) != 0 || len(preResult.UserMessages) != 0 {
		t.Error("empty runner should have no messages")
	}

	StopResult := runner.RunStop(context.Background(), input)
	if StopResult.Blocked {
		t.Error("empty runner should not block")
	}

	hookResult := runner.RunPostToolUse(context.Background(), input)
	if len(hookResult.ModelContext) != 0 || len(hookResult.UserMessages) != 0 {
		t.Error("empty runner should have no messages")
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

	// Pass evener tool name "write_file" — should match after conversion to "Write"
	matched := runner.MatchHooks(plugin.HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for Claude name Write, got %d", len(matched))
	}

	// RunPreToolUse must convert the evener tool name "write_file" to "Write" before
	// matching, so the hook registered for "Write" actually fires. The hook's stdout
	// "matched" routes to UserMessages for non-context PreToolUse events.
	input := Input{
		CWD:           "/tmp",
		HookEventName: "PreToolUse",
		ToolName:      "write_file", // evener name; must be converted to "Write" for matching
	}
	result := runner.RunPreToolUse(context.Background(), input)
	if len(result.UserMessages) == 0 || !strings.Contains(result.UserMessages[0], "matched") {
		t.Errorf("RunPreToolUse with evener tool name 'write_file' should have matched the 'Write' hook and run it; got UserMessages=%v", result.UserMessages)
	}
}

// --- Task 11: Hook Output Parsing ---

func TestParseHookOutput_PlainText(t *testing.T) {
	result := parseHookOutput("some plain text output", 0)
	if result.SystemMessage != "some plain text output" {
		t.Errorf("SystemMessage = %q, want %q", result.SystemMessage, "some plain text output")
	}
	if result.PermissionDecision != "" {
		t.Error("plain text should not set a permission decision")
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
	output := `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"dangerous operation"}}`
	result := parseHookOutput(output, 0)
	if result.PermissionDecision != "deny" {
		t.Errorf("PermissionDecision = %q, want deny", result.PermissionDecision)
	}
	if result.PermissionReason != "dangerous operation" {
		t.Errorf("PermissionReason = %q, want %q", result.PermissionReason, "dangerous operation")
	}
}

func TestParseHookOutput_PermissionDecisionAllow(t *testing.T) {
	o := parseHookOutput(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"looks fine"}}`, 0)
	if o.PermissionDecision != "allow" {
		t.Fatalf("PermissionDecision = %q, want allow", o.PermissionDecision)
	}
	if o.PermissionReason != "looks fine" {
		t.Fatalf("PermissionReason = %q, want \"looks fine\"", o.PermissionReason)
	}
}

func TestParseHookOutput_PermissionDecisionReason(t *testing.T) {
	o := parseHookOutput(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"dangerous"}}`, 0)
	if o.PermissionDecision != "deny" {
		t.Fatalf("PermissionDecision = %q, want deny", o.PermissionDecision)
	}
	if o.PermissionReason != "dangerous" {
		t.Fatalf("PermissionReason = %q, want dangerous", o.PermissionReason)
	}
	if o.SystemMessage != "" {
		t.Fatalf("SystemMessage = %q, want empty (reason routes to PermissionReason)", o.SystemMessage)
	}
}

func TestParseHookOutput_DeprecatedTopLevelDecision(t *testing.T) {
	approve := parseHookOutput(`{"decision":"approve","reason":"ok"}`, 0)
	if approve.PermissionDecision != "allow" {
		t.Fatalf("approve -> PermissionDecision = %q, want allow", approve.PermissionDecision)
	}
	if approve.PermissionReason != "ok" {
		t.Fatalf("top-level reason -> PermissionReason = %q, want ok", approve.PermissionReason)
	}
	both := parseHookOutput(`{"decision":"approve","hookSpecificOutput":{"permissionDecision":"deny"}}`, 0)
	if both.PermissionDecision != "deny" {
		t.Fatalf("preferred should win: PermissionDecision = %q, want deny", both.PermissionDecision)
	}
}

func TestParseHookOutput_SystemMessageIsJSONField(t *testing.T) {
	plain := parseHookOutput("hello", 0)
	if plain.SystemMessageIsJSONField {
		t.Fatal("plain stdout must have SystemMessageIsJSONField=false")
	}
	jsonField := parseHookOutput(`{"systemMessage":"hi"}`, 0)
	if !jsonField.SystemMessageIsJSONField {
		t.Fatal("JSON systemMessage field must have SystemMessageIsJSONField=true")
	}
	errOut := parseHookOutput("boom", 2)
	if errOut.SystemMessageIsJSONField {
		t.Fatal("exit!=0 stderr must have SystemMessageIsJSONField=false")
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
	if result.PermissionDecision != "" {
		t.Error("empty output should not set a permission decision")
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

	// Should not panic when no callback is set, and must still return correct output.
	// SessionStart is a context event: plain stdout routes to ModelContext.
	input := Input{
		CWD:           "/tmp",
		HookEventName: "SessionStart",
	}
	result := runner.RunSessionStart(context.Background(), input)
	if len(result.ModelContext) != 1 || result.ModelContext[0] != "hello" {
		t.Errorf("RunSessionStart without callback: ModelContext = %v, want [hello]", result.ModelContext)
	}
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

// TestHookRunner_SupportedSummary verifies that SupportedSummary counts only hooks
// that can actually run: a hook with an unsupported handler type (http) or an
// invalid-regex matcher is excluded, and an event with zero runnable hooks is
// omitted entirely (whereas Summary counts every registered hook).
func TestHookRunner_SupportedSummary(t *testing.T) {
	runner := newRunner(nil, "")
	// PreToolUse: one runnable command + one dead http handler → runnable count 1.
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "command", Command: "echo a"})
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "http"})
	// PostToolUse: only a command hook with an invalid matcher → runnable count 0.
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo b"})
	// SessionStart: one runnable prompt hook.
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{Matcher: "Write", Type: "prompt", Prompt: "check"})

	sup := runner.SupportedSummary()

	if sup[plugin.HookPreToolUse] != 1 {
		t.Errorf("PreToolUse runnable count = %d, want 1 (http handler excluded)", sup[plugin.HookPreToolUse])
	}
	if _, ok := sup[plugin.HookPostToolUse]; ok {
		t.Errorf("PostToolUse has only an invalid-matcher hook; it must be omitted, got %d", sup[plugin.HookPostToolUse])
	}
	if sup[plugin.HookSessionStart] != 1 {
		t.Errorf("SessionStart runnable count = %d, want 1", sup[plugin.HookSessionStart])
	}
	// Summary, by contrast, still counts every registered hook.
	if got := runner.Summary()[plugin.HookPreToolUse]; got != 2 {
		t.Errorf("Summary PreToolUse = %d, want 2 (registered, including the dead http handler)", got)
	}
}

// --- Phase 1: Characterization tests (lock current behavior) ---

func TestRunPreToolUse_DenyReasonFromPermissionDecisionReason(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("expected deny")
	}
	if r.DenyMessage != "nope" {
		t.Fatalf("DenyMessage = %q, want nope", r.DenyMessage)
	}
}

func TestRunPreToolUse_DeprecatedBlockDenies(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"decision":"block","reason":"legacy"}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("deprecated top-level block must deny PreToolUse")
	}
	if r.DenyMessage != "legacy" {
		t.Fatalf("DenyMessage = %q, want legacy", r.DenyMessage)
	}
}

func TestRunPreToolUse_AskProceeds(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if r.Denied {
		t.Fatal("ask must not deny (evener has no permission prompt)")
	}
	// The unsupported decision must surface a user-visible diagnostic.
	if len(r.UserMessages) != 1 || !strings.Contains(r.UserMessages[0], "ask") {
		t.Fatalf("ask must add a UserMessages diagnostic naming the decision; got %v", r.UserMessages)
	}
}

func TestRunPreToolUse_AllowDoesNotOverrideDeny(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse,
		plugin.RegisteredHook{Matcher: "*", Type: "command", Timeout: 5,
			Command: `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`},
		plugin.RegisteredHook{Matcher: "*", Type: "command", Timeout: 5,
			Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no"}}'`},
	)
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("a deny from any hook must win over a co-occurring allow")
	}
}

func TestRunPreToolUse_NonDenyErrorStderrToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "infra" >&2; exit 1`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if r.Denied {
		t.Fatal("exit 1 is a non-blocking error, not a deny")
	}
	if len(r.ModelContext) == 0 || !strings.Contains(r.ModelContext[0], "infra") {
		t.Fatalf("non-deny PreToolUse error stderr must reach the model: ModelContext = %v", r.ModelContext)
	}
}

func TestRunPreToolUse_DenyStillRoutesAdditionalContext(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no","additionalContext":"ctx-for-model"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied || r.DenyMessage != "no" {
		t.Fatalf("expected deny with reason; Denied=%v DenyMessage=%q", r.Denied, r.DenyMessage)
	}
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "ctx-for-model" {
		t.Fatalf("denied call must still deliver additionalContext to the model; ModelContext = %v", r.ModelContext)
	}
	if len(r.UserMessages) != 0 {
		t.Fatalf("deny reason must not leak to UserMessages; got %v", r.UserMessages)
	}
}

func TestRunPreToolUse_DenySystemMessageToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"contact security","hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"blocked"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied || r.DenyMessage != "blocked" {
		t.Fatalf("expected deny with reason 'blocked'; Denied=%v DenyMessage=%q", r.Denied, r.DenyMessage)
	}
	// A top-level systemMessage is user-visible guidance distinct from the deny
	// reason; a deny must not suppress it.
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "contact security" {
		t.Fatalf("deny must still surface the systemMessage to the user; UserMessages = %v", r.UserMessages)
	}
}

func TestRunPreToolUse_DeferProceeds(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"defer"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if r.Denied {
		t.Fatal("defer must not deny")
	}
	if len(r.UserMessages) != 1 || !strings.Contains(r.UserMessages[0], "defer") {
		t.Fatalf("defer must add a UserMessages diagnostic naming the decision; got %v", r.UserMessages)
	}
}

func TestRouting_JSONSystemMessageOnContextEventToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"user-msg"}'`,
	})
	r := runner.RunSessionStart(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionStart"})
	// On a context event, plain stdout reaches the model, but the JSON systemMessage
	// field is still user-visible only.
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "user-msg" {
		t.Fatalf("JSON systemMessage on a context event must go to the user; UserMessages = %v", r.UserMessages)
	}
	if len(r.ModelContext) != 0 {
		t.Fatalf("JSON systemMessage must not reach the model; ModelContext = %v", r.ModelContext)
	}
}

func TestRunNotification_Routing(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"additionalContext":"to-model"}}'`,
	})
	r := runner.RunNotification(context.Background(), Input{CWD: "/tmp", HookEventName: "Notification"})
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "to-model" {
		t.Fatalf("Notification additionalContext must reach the model; ModelContext = %v", r.ModelContext)
	}
}

func TestRunNotification_PlainStdoutToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo notice`,
	})
	r := runner.RunNotification(context.Background(), Input{CWD: "/tmp", HookEventName: "Notification"})
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "notice" {
		t.Fatalf("Notification plain stdout must go to the user; UserMessages = %v", r.UserMessages)
	}
	if len(r.ModelContext) != 0 {
		t.Fatalf("Notification plain stdout must not reach the model; ModelContext = %v", r.ModelContext)
	}
}

func TestRunPreCompact_Routing(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"additionalContext":"to-model"}}'`,
	})
	r := runner.RunPreCompactFor(context.Background(), Input{CWD: "/tmp", HookEventName: "PreCompact"}, "auto")
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "to-model" {
		t.Fatalf("PreCompact additionalContext must reach the model; ModelContext = %v", r.ModelContext)
	}
}

func TestRunPreCompact_PlainStdoutToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo compacting`,
	})
	r := runner.RunPreCompactFor(context.Background(), Input{CWD: "/tmp", HookEventName: "PreCompact"}, "auto")
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "compacting" {
		t.Fatalf("PreCompact plain stdout must go to the user; UserMessages = %v", r.UserMessages)
	}
}

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

// TestMatchHooks_InvalidMatcherEmitsWarning verifies that an invalid-regex matcher
// produces exactly one loud, sanitized diagnostic naming the plugin, event, and
// matcher — now surfaced at LOAD time via Validate rather than at dispatch (the
// dispatch-time emit was the root of the Notification-hook recursion and the
// per-tool-call warning storm). MatchHooks itself must skip silently and emit
// nothing. The matcher string is the only matcher-derived data exposed; no
// payload is leaked.
func TestMatchHooks_InvalidMatcherEmitsWarning(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher:    "(", // invalid regex
		Type:       "command",
		Command:    "echo should-not-run",
		Timeout:    5,
		PluginName: "broken-plugin",
	})

	// The dispatch path must be silent: skip the hook, emit nothing.
	var emitted int
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		emitted++
	})
	if got := r.MatchHooks(plugin.HookPreToolUse, "Bash"); len(got) != 0 {
		t.Fatalf("invalid regex: hook must be skipped; got %d matches", len(got))
	}
	if emitted != 0 {
		t.Fatalf("MatchHooks must not emit on dispatch; got %d events", emitted)
	}

	// The diagnostic is surfaced once at load time via Validate.
	diags := r.Validate()
	if len(diags) != 1 {
		t.Fatalf("expected exactly 1 invalid-matcher diagnostic, got %d", len(diags))
	}
	d := diags[0]
	if d.PluginName != "broken-plugin" {
		t.Errorf("PluginName = %q, want %q", d.PluginName, "broken-plugin")
	}
	if d.Event != string(plugin.HookPreToolUse) {
		t.Errorf("Event = %q, want %q", d.Event, plugin.HookPreToolUse)
	}
	if !strings.Contains(d.Message, "(") {
		t.Errorf("diagnostic message %q should name the offending matcher", d.Message)
	}
	if !strings.Contains(strings.ToLower(d.Message), "matcher") {
		t.Errorf("diagnostic message %q should explain the matcher is invalid", d.Message)
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

// --- Task 3: Routing tests ---

func TestRouting_AdditionalContextToModel_SystemMessageToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"to-user","hookSpecificOutput":{"additionalContext":"to-model"}}'`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "to-model" {
		t.Fatalf("ModelContext = %v, want [to-model]", r.ModelContext)
	}
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "to-user" {
		t.Fatalf("UserMessages = %v, want [to-user]", r.UserMessages)
	}
}

func TestRouting_PostToolUseExit2StderrToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "boom" >&2; exit 2`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.ModelContext) == 0 || !strings.Contains(r.ModelContext[0], "boom") {
		t.Fatalf("PostToolUse exit-2 stderr must reach the model: ModelContext = %v", r.ModelContext)
	}
	if len(r.UserMessages) != 0 {
		t.Fatalf("PostToolUse exit-2 stderr must not go to the user: UserMessages = %v", r.UserMessages)
	}
}

func TestRouting_StopExit2StderrToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "stay" >&2; exit 2`,
	})
	r := runner.RunStop(context.Background(), Input{CWD: "/tmp", HookEventName: "Stop"})
	if !r.Blocked {
		t.Fatal("Stop exit 2 must block")
	}
	if len(r.ModelContext) == 0 || !strings.Contains(r.ModelContext[0], "stay") {
		t.Fatalf("Stop exit-2 stderr must reach the model: ModelContext = %v", r.ModelContext)
	}
}

func TestRouting_ContextEventPlainStdoutToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo bootstrap`,
	})
	r := runner.RunSessionStart(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionStart"})
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "bootstrap" {
		t.Fatalf("SessionStart plain stdout must reach the model: ModelContext = %v", r.ModelContext)
	}
}

func TestRouting_NonContextPlainStdoutToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo logged`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "logged" {
		t.Fatalf("PostToolUse plain stdout must go to the user: UserMessages = %v", r.UserMessages)
	}
	if len(r.ModelContext) != 0 {
		t.Fatalf("PostToolUse plain stdout must not reach the model: ModelContext = %v", r.ModelContext)
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

// TestSessionStartFailureIsOneSummarizedWarning pins the rendering of a FAILED
// SessionStart hook. A SessionStart hook's output is the session's FIRST steering
// input, so a hook that dies — the exit-126 sandbox denial that motivated this,
// or any other failure — must never dump raw shell stderr into the model's
// context. It surfaces as exactly ONE user-facing warning line naming the hook
// and its exit code.
func TestSessionStartFailureIsOneSummarizedWarning(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, PluginName: "superpowers",
		Command: `printf 'line one\nline two\n' >&2; exit 126`,
	})
	r := runner.RunSessionStart(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionStart"})

	if len(r.ModelContext) != 0 {
		t.Fatalf("a failed SessionStart hook must not steer the model at all; ModelContext = %q", r.ModelContext)
	}
	if len(r.UserMessages) != 1 {
		t.Fatalf("a failed SessionStart hook must surface exactly one warning; UserMessages = %q", r.UserMessages)
	}
	line := r.UserMessages[0]
	if strings.Contains(line, "\n") {
		t.Errorf("the warning must be ONE line, got:\n%s", line)
	}
	if !strings.HasPrefix(line, "SessionStart hook superpowers failed (exit 126)") {
		t.Errorf("the warning must name the event, the hook, and the exit code; got %q", line)
	}
	if strings.Contains(line, "line two") {
		t.Errorf("the warning must not carry the hook's whole stderr; got %q", line)
	}
}

// TestSessionStartFailureNamesAnUnattributedHook: a hook with no plugin name
// still gets a legible one-line warning rather than a bare exit code.
func TestSessionStartFailureNamesAnUnattributedHook(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `exit 3`,
	})
	r := runner.RunSessionStart(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionStart"})
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "SessionStart hook failed (exit 3)" {
		t.Fatalf("UserMessages = %q, want one %q", r.UserMessages, "SessionStart hook failed (exit 3)")
	}
	if len(r.ModelContext) != 0 {
		t.Fatalf("ModelContext = %q, want none", r.ModelContext)
	}
}

// TestToolEventFailureStillFeedsTheModel guards the blast radius: the summarized
// SessionStart line is a SessionStart-only change. The Claude-compatible contract
// that a tool event's exit-2 stderr reaches the model (so the model can correct
// itself) is untouched.
func TestToolEventFailureStillFeedsTheModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, PluginName: "superpowers",
		Command: `echo 'raw stderr for the model' >&2; exit 2`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "shell"})
	if len(r.ModelContext) != 1 || !strings.Contains(r.ModelContext[0], "raw stderr for the model") {
		t.Fatalf("PostToolUse exit-2 stderr must still reach the model; ModelContext = %q", r.ModelContext)
	}
}

// --- Claude hook lifecycle compat: shared input layer ---

func TestInput_EffortSerializesAsLevelObject(t *testing.T) {
	b, err := json.Marshal(Input{Effort: &Effort{Level: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	eff, ok := m["effort"].(map[string]any)
	if !ok || eff["level"] != "high" {
		t.Fatalf("effort = %v, want {\"level\":\"high\"}", m["effort"])
	}
}

func TestInput_EffortAbsentWhenUnset(t *testing.T) {
	b, _ := json.Marshal(Input{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, present := m["effort"]; present {
		t.Fatalf("effort must be absent, got %v", m["effort"])
	}
}

// TestInput_EventFieldsJSONNames pins the Claude field names the later event
// tasks fill. A rename here silently breaks every hook that reads them.
func TestInput_EventFieldsJSONNames(t *testing.T) {
	zero := false
	var dur int64
	b, err := json.Marshal(Input{
		Trigger:              "manual",
		CustomInstructions:   new("condense"),
		CompactSummary:       "summary",
		StopHookActive:       &zero,
		LastAssistantMessage: "last",
		Error:                "boom",
		ErrorDetails:         "429",
		IsInterrupt:          &zero,
		DurationMS:           &dur,
		ToolCalls:            []BatchToolCall{{ToolName: "Bash", ToolUseID: "id-1", ToolResponse: "ok"}},
		NotificationType:     "warning",
		Title:                "title",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"trigger":"manual"`, `"custom_instructions":"condense"`, `"compact_summary":"summary"`,
		`"stop_hook_active":false`, `"last_assistant_message":"last"`, `"error":"boom"`,
		`"error_details":"429"`, `"is_interrupt":false`, `"duration_ms":0`,
		`"tool_calls":[{"tool_name":"Bash","tool_use_id":"id-1","tool_response":"ok"}]`,
		`"notification_type":"warning"`, `"title":"title"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}

	// Every event field is omitempty, so an Input carrying none of them emits
	// none of their keys. Claude hooks distinguish absent from zero.
	empty, err := json.Marshal(Input{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"trigger"`, `"custom_instructions"`, `"compact_summary"`, `"stop_hook_active"`,
		`"last_assistant_message"`, `"error"`, `"error_details"`, `"is_interrupt"`,
		`"duration_ms"`, `"tool_calls"`, `"notification_type"`, `"title"`,
	} {
		if strings.Contains(string(empty), key) {
			t.Errorf("%s must be omitted from an empty Input, got %s", key, empty)
		}
	}
}

// TestHonorsContinue names the five events where Claude discards
// continue/stopReason, and proves every other event honors it.
func TestHonorsContinue(t *testing.T) {
	for _, event := range []plugin.HookEvent{
		plugin.HookStopFailure, plugin.HookNotification, plugin.HookPreCompact,
		plugin.HookPostCompact, plugin.HookSessionEnd,
	} {
		if HonorsContinue(event) {
			t.Errorf("%s must discard continue:false", event)
		}
	}
	for _, event := range []plugin.HookEvent{
		plugin.HookPreToolUse, plugin.HookPostToolUse, plugin.HookPostToolBatch,
		plugin.HookPostToolUseFailure, plugin.HookUserPromptSubmit, plugin.HookSessionStart,
		plugin.HookStop, plugin.HookSubagentStop, plugin.HookSubagentStart,
	} {
		if !HonorsContinue(event) {
			t.Errorf("%s must honor continue:false", event)
		}
	}
}

func TestRunNotification_ContinueFalseIsDiscarded(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookNotification, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"no more"}'`})
	out := r.RunNotification(context.Background(), Input{HookEventName: "Notification"})
	if out.Stopped {
		t.Fatalf("Notification must discard continue:false")
	}
}

func TestRunPostToolUse_ContinueFalseStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop here"}'`})
	out := r.RunPostToolUse(context.Background(), Input{HookEventName: "PostToolUse"})
	if !out.Stopped {
		t.Fatalf("PostToolUse must honor continue:false")
	}
	if out.StopReason == "" {
		t.Fatalf("StopReason must be carried")
	}
}

// TestRunPreToolUse_ContinueFalseStops covers the PreToolUse aggregation site:
// a stop is recorded there independently of the event's own deny decision.
func TestRunPreToolUse_ContinueFalseStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop"}'`})
	out := r.RunPreToolUse(context.Background(), Input{HookEventName: "PreToolUse", ToolName: "Bash"})
	if !out.Stopped {
		t.Fatalf("PreToolUse must honor continue:false")
	}
	if out.StopReason != "stop" {
		t.Fatalf("StopReason = %q, want %q", out.StopReason, "stop")
	}
}

// TestRunStop_ContinueFalseStops covers the Stop aggregation site.
func TestRunStop_ContinueFalseStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookStop, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop"}'`})
	out := r.RunStop(context.Background(), Input{HookEventName: "Stop"})
	if !out.Stopped {
		t.Fatalf("Stop must honor continue:false")
	}
	if out.StopReason != "stop" {
		t.Fatalf("StopReason = %q, want %q", out.StopReason, "stop")
	}
}

// TestRunStopFailureFor_MatchesErrorName proves the event's matcher target is
// the error name: a hook matched on "rate_limit" fires for a rate limit and not
// for another name.
func TestRunStopFailureFor_MatchesErrorName(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "failure-ran")
	r := newRunner(nil, "")
	r.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "rate_limit", Type: "command", Timeout: 5,
		Command: "cat >> " + shellquote.Literal(marker),
	})

	if got := r.RunStopFailureFor(context.Background(), Input{HookEventName: "StopFailure"}, "server_error"); !zeroRunResult(got) {
		t.Fatalf("RunStopFailureFor(server_error) = %+v, want a zero result", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a rate_limit matcher must not fire for server_error; stat err = %v", err)
	}

	_ = r.RunStopFailureFor(context.Background(), Input{HookEventName: "StopFailure"}, "rate_limit")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("rate_limit matcher did not fire: %v", err)
	}
	if !strings.Contains(string(data), `"hook_event_name":"StopFailure"`) {
		t.Fatalf("hook input = %s, want the StopFailure event name", data)
	}
}

// TestRunStopFailureFor_DiscardsOutputAndNeverStops proves the event is
// fire-and-discard: a hook's continue:false, systemMessage, additionalContext,
// and exit 2 all leave the run result free of stops, model context, and user
// messages. Only terminalSequence is carried, parsed for a delivery site it
// does not yet have.
func TestRunStopFailureFor_DiscardsOutputAndNeverStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `printf '%s' '{"continue":false,"stopReason":"stop","systemMessage":"user","hookSpecificOutput":{"additionalContext":"model"},"terminalSequence":"done"}'`,
	})
	r.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "failure-hook-stderr" >&2; exit 2`,
	})

	got := r.RunStopFailureFor(context.Background(), Input{HookEventName: "StopFailure"}, "rate_limit")
	if got.Stopped || got.StopReason != "" {
		t.Fatalf("Stopped = %v (%q), want no stop: continue:false and exit 2 are discarded", got.Stopped, got.StopReason)
	}
	if len(got.ModelContext) != 0 || len(got.UserMessages) != 0 {
		t.Fatalf("ModelContext = %q, UserMessages = %q, want all output discarded", got.ModelContext, got.UserMessages)
	}
	if len(got.TerminalSequences) != 1 || got.TerminalSequences[0] != "done" {
		t.Fatalf("TerminalSequences = %q, want the parsed terminal sequence", got.TerminalSequences)
	}
}

func zeroRunResult(r RunResult) bool {
	return !r.Stopped && r.StopReason == "" && len(r.TerminalSequences) == 0 &&
		len(r.ModelContext) == 0 && len(r.UserMessages) == 0
}

// TestRunPostToolUseFailure_DispatchesAndRoutes proves the failure event has a
// runner that dispatches its own hooks (never PostToolUse's), matches the
// Bash-style name, and routes additionalContext to the model and a JSON
// systemMessage to the user — the same routing PostToolUse uses.
func TestRunPostToolUseFailure_DispatchesAndRoutes(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolUseFailure, plugin.RegisteredHook{
		Matcher: "Bash", Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"failure-to-user","hookSpecificOutput":{"hookEventName":"PostToolUseFailure","additionalContext":"failure-to-model"}}'`,
	})
	// A PostToolUse hook in the same runner must not fire for the failure event.
	r.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo '{"systemMessage":"success-hook-fired"}'`,
	})

	out := r.RunPostToolUseFailure(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUseFailure", ToolName: "Bash"})
	if len(out.ModelContext) != 1 || out.ModelContext[0] != "failure-to-model" {
		t.Fatalf("ModelContext = %v, want [failure-to-model]", out.ModelContext)
	}
	if len(out.UserMessages) != 1 || out.UserMessages[0] != "failure-to-user" {
		t.Fatalf("UserMessages = %v, want [failure-to-user]", out.UserMessages)
	}
	for _, msg := range append(out.ModelContext, out.UserMessages...) {
		if strings.Contains(msg, "success-hook-fired") {
			t.Fatalf("RunPostToolUseFailure dispatched a PostToolUse hook: %v", msg)
		}
	}
}

// TestRunPostToolUseFailure_HonorsContinueFalse keeps the failure event in the
// honored-continue set, matching Claude's table.
func TestRunPostToolUseFailure_HonorsContinueFalse(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolUseFailure, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop after failure"}'`})
	out := r.RunPostToolUseFailure(context.Background(), Input{HookEventName: "PostToolUseFailure", ToolName: "Bash"})
	if !out.Stopped {
		t.Fatal("PostToolUseFailure must honor continue:false")
	}
	if out.StopReason != "stop after failure" {
		t.Fatalf("StopReason = %q, want %q", out.StopReason, "stop after failure")
	}
}

// TestRunSubagentStop_ContinueFalseStops covers the SubagentStop aggregation
// site, which shares runStopEvent but has its own event key and block row.
func TestRunSubagentStop_ContinueFalseStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStop, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop"}'`})
	out := r.RunSubagentStopFor(context.Background(), Input{HookEventName: "SubagentStop"}, "subagent")
	if !out.Stopped {
		t.Fatalf("SubagentStop must honor continue:false")
	}
	if out.StopReason != "stop" {
		t.Fatalf("StopReason = %q, want %q", out.StopReason, "stop")
	}
}

// TestRunSubagentStopFor_MatchesAgentType proves SubagentStop's matcher target is
// the child's agent type, the same value SubagentStart matches on: a hook for one
// type fires and a hook for another does not.
func TestRunSubagentStopFor_MatchesAgentType(t *testing.T) {
	r := newRunner(nil, "")
	hit := filepath.Join(t.TempDir(), "agent-type-hit")
	miss := filepath.Join(t.TempDir(), "agent-type-miss")
	r.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "toolsmith:toolsmith", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(hit),
	})
	r.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "subagent", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(miss),
	})
	out := r.RunSubagentStopFor(context.Background(), Input{HookEventName: "SubagentStop"}, "toolsmith:toolsmith")
	if out.Blocked || out.Stopped {
		t.Fatalf("a plain touch hook must not block or stop: %+v", out)
	}
	if _, err := os.Stat(hit); err != nil {
		t.Fatalf("the toolsmith:toolsmith hook did not fire: %v", err)
	}
	if _, err := os.Stat(miss); !os.IsNotExist(err) {
		t.Fatalf("a subagent-matched hook fired for a plugin agent; stat err = %v", err)
	}
}

// TestRunSessionEndFor_MatchesReason proves SessionEnd's matcher target is the
// Claude session end reason: a clear close fires only the clear hook, a plain
// close only the other hook.
func TestRunSessionEndFor_MatchesReason(t *testing.T) {
	dir := t.TempDir()
	clearMarker := filepath.Join(dir, "clear")
	otherMarker := filepath.Join(dir, "other")
	r := newRunner(nil, "")
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "clear", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(clearMarker),
	})
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "other", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(otherMarker),
	})

	r.RunSessionEndFor(context.Background(), Input{HookEventName: "SessionEnd", Reason: "clear"}, "clear")
	if _, err := os.Stat(clearMarker); err != nil {
		t.Fatalf("the clear-matched hook did not fire for reason clear: %v", err)
	}
	if _, err := os.Stat(otherMarker); !os.IsNotExist(err) {
		t.Fatalf("the other-matched hook fired for reason clear; stat err = %v", err)
	}
}

// TestRunSessionEndFor_MatchesPromptInputExit proves the shutdown close's reason
// is a matcher target too, and that the never-sent words stay separate from it.
func TestRunSessionEndFor_MatchesPromptInputExit(t *testing.T) {
	dir := t.TempDir()
	promptExit := filepath.Join(dir, "prompt-input-exit")
	resume := filepath.Join(dir, "resume")
	logout := filepath.Join(dir, "logout")
	r := newRunner(nil, "")
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "prompt_input_exit", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(promptExit),
	})
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "resume", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(resume),
	})
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "logout", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(logout),
	})

	r.RunSessionEndFor(context.Background(), Input{HookEventName: "SessionEnd", Reason: "prompt_input_exit"}, "prompt_input_exit")
	if _, err := os.Stat(promptExit); err != nil {
		t.Fatalf("the prompt_input_exit-matched hook did not fire: %v", err)
	}
	for name, path := range map[string]string{"resume": resume, "logout": logout} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a %s-matched hook fired for prompt_input_exit; stat err = %v", name, err)
		}
	}
}

// TestRunStop_IgnoresConfiguredMatcher proves Claude's "no matcher" for Stop: a
// hook configured with an exact tool matcher still fires, because there is no
// tool to match against at a turn boundary. Consulting the matcher would skip
// every such hook silently, which is what the old target did.
func TestRunStop_IgnoresConfiguredMatcher(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stop-ran")
	r := newRunner(nil, "")
	r.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "Bash", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(marker),
	})
	out := r.RunStop(context.Background(), Input{HookEventName: "Stop"})
	if out.Blocked || out.Stopped {
		t.Fatalf("a plain touch hook must not block or stop: %+v", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a Bash-matched Stop hook did not fire at a turn boundary: %v", err)
	}
}

// TestRunNotification_MatchesNotificationType proves Notification's matcher
// target is the input's notification_type: a hook for the emitted type fires and
// one for a Claude type evener never emits does not.
func TestRunNotification_MatchesNotificationType(t *testing.T) {
	dir := t.TempDir()
	warningMarker := filepath.Join(dir, "warning")
	idleMarker := filepath.Join(dir, "idle")
	r := newRunner(nil, "")
	r.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "warning", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(warningMarker),
	})
	r.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "idle_prompt", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(idleMarker),
	})

	r.RunNotification(context.Background(), Input{HookEventName: "Notification", NotificationType: "warning"})
	if _, err := os.Stat(warningMarker); err != nil {
		t.Fatalf("the warning-matched hook did not fire: %v", err)
	}
	if _, err := os.Stat(idleMarker); !os.IsNotExist(err) {
		t.Fatalf("an idle_prompt-matched hook fired for a warning; stat err = %v", err)
	}
}

// TestRunPostToolBatch_IgnoresConfiguredMatcher proves Claude's "There is no
// matcher for this event": a configured matcher naming one tool must not skip
// a PostToolBatch hook, because the event describes a whole batch.
func TestRunPostToolBatch_IgnoresConfiguredMatcher(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Matcher: "Bash", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"batch-note"}}'`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if len(got.ModelContext) != 1 {
		t.Fatalf("a matcher must not skip PostToolBatch; ModelContext = %v", got.ModelContext)
	}
}

// TestRunPostToolBatch_ToolCallsMarshal pins the event's input: the batch's
// tool_calls array carries each call's Claude tool name, id, and the
// model-facing tool_response, and an exit-2 hook blocks the round.
func TestRunPostToolBatch_ToolCallsMarshal(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "batch-input.json")
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `cat > ` + shellquote.Literal(capture) + `; printf 'stop the loop\n' >&2; exit 2`})

	got := r.RunPostToolBatch(context.Background(), Input{
		CWD:           dir,
		HookEventName: "PostToolBatch",
		ToolCalls: []BatchToolCall{{
			ToolName:     "Read",
			ToolInput:    map[string]any{"file_path": "/tmp/x"},
			ToolUseID:    "call_1",
			ToolResponse: "file text",
		}},
	})
	if !got.Stopped {
		t.Fatalf("exit 2 marks the batch blocked, got %+v", got)
	}
	if got.StopReason != "stop the loop" {
		t.Fatalf("StopReason = %q, want the exit-2 stderr", got.StopReason)
	}
	for _, msg := range got.ModelContext {
		if strings.Contains(msg, "stop the loop") {
			t.Fatalf("the exit-2 stderr is the block reason, not model context: %v", got.ModelContext)
		}
	}

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured hook input: %v", err)
	}
	var captured struct {
		HookEventName string `json:"hook_event_name"`
		ToolCalls     []struct {
			ToolName     string         `json:"tool_name"`
			ToolInput    map[string]any `json:"tool_input"`
			ToolUseID    string         `json:"tool_use_id"`
			ToolResponse string         `json:"tool_response"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("decode captured hook input: %v (raw: %s)", err, raw)
	}
	if captured.HookEventName != "PostToolBatch" {
		t.Errorf("hook_event_name = %q, want PostToolBatch", captured.HookEventName)
	}
	if len(captured.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want one entry", captured.ToolCalls)
	}
	call := captured.ToolCalls[0]
	if call.ToolName != "Read" || call.ToolUseID != "call_1" || call.ToolResponse != "file text" {
		t.Errorf("tool_calls[0] = %+v, want Read/call_1/file text", call)
	}
	if got := call.ToolInput["file_path"]; got != "/tmp/x" {
		t.Errorf("tool_calls[0].tool_input.file_path = %v, want /tmp/x", got)
	}
}

// TestRunPostToolBatch_Exit2Blocks keeps the runner's exit-2 contract pinned at
// the runner rather than only through the exitBehavior table.
func TestRunPostToolBatch_Exit2Blocks(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo "stop the loop" >&2; exit 2`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if !got.Stopped {
		t.Fatalf("exit 2 must stop the loop, got %+v", got)
	}
}

// TestRunPostToolBatch_DecisionBlockStops covers the JSON block form, whose
// reason is the top-level reason field rather than exit-2 stderr.
func TestRunPostToolBatch_DecisionBlockStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"decision":"block","reason":"batch says stop"}'`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if !got.Stopped || got.StopReason != "batch says stop" {
		t.Fatalf("decision:block = %+v, want Stopped with the reason", got)
	}
}

// TestRunPostToolBatch_ContinueFalseCarriesReason covers continue:false with a
// top-level reason but no stopReason: spec §4.4 says the block message comes
// from JSON reason/stopReason, so the reason must reach StopReason rather than
// falling back to the dispatch site's generic line.
func TestRunPostToolBatch_ContinueFalseCarriesReason(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"reason":"batch says stop"}'`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if !got.Stopped {
		t.Fatalf("continue:false must stop the loop, got %+v", got)
	}
	if got.StopReason != "batch says stop" {
		t.Fatalf("StopReason = %q, want the JSON reason %q", got.StopReason, "batch says stop")
	}
}

// TestRunPostToolBatch_ReasonIsOneLine bounds the reason: multi-line shell
// stderr must not dump a wall of text into the user warning and the steering
// turn it becomes downstream.
func TestRunPostToolBatch_ReasonIsOneLine(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `printf 'first line\nsecond line\nthird line\n' >&2; exit 2`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if !got.Stopped {
		t.Fatalf("exit 2 must stop the loop, got %+v", got)
	}
	if got.StopReason != "first line" {
		t.Fatalf("StopReason = %q, want just the first line %q", got.StopReason, "first line")
	}
}

// TestSupportedSummary_PostToolBatchMatcherIsRunnable proves the runnable-hook
// count does not probe a matcher that PostToolBatch never consults: a hook whose
// matcher names no tool still fires, so it must be counted.
func TestSupportedSummary_PostToolBatchMatcherIsRunnable(t *testing.T) {
	r := newRunner(nil, "")
	// "(" is the only value the old matchTarget(matcher, "") probe rejected, so
	// this is a real red-before-green: "Bash" was always runnable.
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo batch"})
	if got := r.SupportedSummary()[plugin.HookPostToolBatch]; got != 1 {
		t.Fatalf("SupportedSummary[PostToolBatch] = %d, want 1: the matcher is not consulted", got)
	}
}

// TestValidate_PostToolBatchMatcherIsNotDiagnosed proves the load-time warning
// agrees with dispatch: a PostToolBatch matcher that is not a valid regex never
// skips the hook, so it must not be reported as one that "will not fire".
func TestValidate_PostToolBatchMatcherIsNotDiagnosed(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo batch", PluginName: "batch-plugin"})
	if diags := r.Validate(); len(diags) != 0 {
		t.Fatalf("Validate() = %+v, want none: no matcher can skip PostToolBatch", diags)
	}
}

// TestValidate_StopMatcherIsNotDiagnosed proves the load-time warning agrees with
// dispatch for Stop too: RunStop bypasses matching, so an uncompilable matcher
// never skips the hook and "will not fire" would be a false alarm. This is the
// exact regression the review found — Validate and hookCanRun probed a matcher
// that dispatch never consulted.
func TestValidate_StopMatcherIsNotDiagnosed(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookStop, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo stop", PluginName: "stop-plugin"})
	if diags := r.Validate(); len(diags) != 0 {
		t.Fatalf("Validate() = %+v, want none: no matcher can skip Stop", diags)
	}
}

// TestSupportedSummary_StopMatcherIsRunnable proves the runnable-hook count (the
// /status number) agrees with dispatch for Stop: a "(" matcher still fires, so
// the hook is counted rather than omitted.
func TestSupportedSummary_StopMatcherIsRunnable(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookStop, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo stop"})
	if got := r.SupportedSummary()[plugin.HookStop]; got != 1 {
		t.Fatalf("SupportedSummary[Stop] = %d, want 1: the matcher is not consulted", got)
	}
}

// TestSupportedSummary_UserPromptSubmitMatcherIsRunnable is the same statement
// for UserPromptSubmit, whose matcher dispatch also ignores.
func TestSupportedSummary_UserPromptSubmitMatcherIsRunnable(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookUserPromptSubmit, plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo prompt"})
	if got := r.SupportedSummary()[plugin.HookUserPromptSubmit]; got != 1 {
		t.Fatalf("SupportedSummary[UserPromptSubmit] = %d, want 1: the matcher is not consulted", got)
	}
}

// TestRunUserPromptSubmit_IgnoresConfiguredMatcher proves Claude's "no matcher"
// for UserPromptSubmit: a hook with an exact, non-empty matcher ("startup") still
// fires, because there is no value to match it against. Before this, the runner
// matched against an empty target, so an exact matcher never fired.
func TestRunUserPromptSubmit_IgnoresConfiguredMatcher(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "user-prompt-ran")
	r := newRunner(nil, "")
	r.Add(plugin.HookUserPromptSubmit, plugin.RegisteredHook{
		Matcher: "startup", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(marker),
	})
	out := r.RunUserPromptSubmit(context.Background(), Input{HookEventName: "UserPromptSubmit", Prompt: "hello"})
	if out.Stopped {
		t.Fatalf("a plain touch hook must not stop the prompt: %+v", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a startup-matched UserPromptSubmit hook did not fire: %v", err)
	}
}

// TestRunPostToolBatch_EmitsHookLifecycleEvents keeps the HOOK_START/HOOK_END
// announcements on the bypass path; runMatched owns them for every event.
func TestRunPostToolBatch_EmitsHookLifecycleEvents(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5, Command: "true"})
	var starts, ends int
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		switch kind {
		case events.EventHookStart:
			if start, ok := data.(events.HookStartData); ok && start.Event == string(plugin.HookPostToolBatch) {
				starts++
			}
		case events.EventHookEnd:
			if end, ok := data.(events.HookEndData); ok && end.Event == string(plugin.HookPostToolBatch) {
				ends++
			}
		}
	})
	r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if starts != 1 || ends != 1 {
		t.Fatalf("hook lifecycle events = %d start / %d end, want 1/1", starts, ends)
	}
}

// TestRunPreCompactFor_MatchesTrigger proves PreCompact's matcher target is the
// fold's trigger: a "manual" hook fires for /compact and not for a pressure
// fold.
func TestRunPreCompactFor_MatchesTrigger(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "manual", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"PreCompact","additionalContext":"manual-note"}}'`})
	if got := r.RunPreCompactFor(context.Background(), Input{}, "manual"); len(got.ModelContext) != 1 {
		t.Fatalf("manual must match, got %v", got.ModelContext)
	}
	if got := r.RunPreCompactFor(context.Background(), Input{}, "auto"); len(got.ModelContext) != 0 {
		t.Fatalf("auto must not match a manual hook, got %v", got.ModelContext)
	}
}

// TestRunPreCompactFor_MatchesTriggerWithoutJSONEventName proves the trigger
// alone decides the match, independent of any input field.
func TestRunPreCompactFor_MatchesTriggerWithoutJSONEventName(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "auto|manual", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"PreCompact","additionalContext":"either-note"}}'`})
	if got := r.RunPreCompactFor(context.Background(), Input{}, "auto"); len(got.ModelContext) != 1 {
		t.Fatalf("auto must match an either-trigger hook, got %v", got.ModelContext)
	}
	if got := r.RunPreCompactFor(context.Background(), Input{}, "manual"); len(got.ModelContext) != 1 {
		t.Fatalf("manual must match an either-trigger hook, got %v", got.ModelContext)
	}
}

// TestRunPostCompactFor_DiscardsOutputAndContinue pins PostCompact's contract:
// it has no decision control, and systemMessage, additionalContext, and
// continue:false are all discarded.
func TestRunPostCompactFor_DiscardsOutputAndContinue(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostCompact, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"x","continue":false,"hookSpecificOutput":{"hookEventName":"PostCompact","additionalContext":"y"}}'`})
	got := r.RunPostCompactFor(context.Background(), Input{}, "auto")
	if len(got.ModelContext) != 0 || len(got.UserMessages) != 0 || got.Stopped {
		t.Fatalf("PostCompact output must be discarded, got %+v", got)
	}
}

// TestRunPostCompactFor_MatchesTrigger proves PostCompact matches the trigger
// the same way PreCompact does, so a hook pair with one matcher stays
// consistent.
func TestRunPostCompactFor_MatchesTrigger(t *testing.T) {
	r := newRunner(nil, "")
	var runs int
	r.Add(plugin.HookPostCompact, plugin.RegisteredHook{Matcher: "^manual$", Type: "command", Timeout: 5,
		Command: "true"})
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if kind == events.EventHookStart {
			if start, ok := data.(events.HookStartData); ok && start.Event == string(plugin.HookPostCompact) {
				runs++
			}
		}
	})
	r.RunPostCompactFor(context.Background(), Input{}, "auto")
	if runs != 0 {
		t.Fatalf("an auto fold ran a manual PostCompact %d time(s)", runs)
	}
	r.RunPostCompactFor(context.Background(), Input{}, "manual")
	if runs != 1 {
		t.Fatalf("a manual fold ran the manual PostCompact %d time(s), want 1", runs)
	}
}

// TestRunPostCompactFor_FailureIsUserOnly proves the one output PostCompact
// keeps: a failed hook's summarized warning — which names the event, the hook,
// and its exit code before the first stderr line — reaches the operator, and
// never the model.
func TestRunPostCompactFor_FailureIsUserOnly(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostCompact, plugin.RegisteredHook{Type: "command", Timeout: 5, PluginName: "compact-plugin",
		Command: `printf 'first line\nsecond line\n' >&2; exit 2`})
	got := r.RunPostCompactFor(context.Background(), Input{}, "auto")
	if len(got.ModelContext) != 0 {
		t.Fatalf("PostCompact failure must not reach the model, got %v", got.ModelContext)
	}
	if len(got.UserMessages) != 1 {
		t.Fatalf("PostCompact failure must reach the user as one line, got %v", got.UserMessages)
	}
	line := got.UserMessages[0]
	for _, want := range []string{"PostCompact hook", "compact-plugin", "exit 2", "first line"} {
		if !strings.Contains(line, want) {
			t.Fatalf("failure line %q must name the event, hook, exit code, and stderr; missing %q", line, want)
		}
	}
	if strings.Contains(line, "second line") {
		t.Fatalf("failure line %q must be bounded to the first output line", line)
	}
	if got.Stopped {
		t.Fatalf("PostCompact has no decision control, got Stopped")
	}
}
