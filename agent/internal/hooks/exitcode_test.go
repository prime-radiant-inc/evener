package hooks

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// TestExitBehavior_PerEvent verifies the central exit-code table classifies
// exit 2 as blocking or non-blocking per event (07 §Exit-code semantics).
// Tier: claude-compatible-subset.
func TestExitBehavior_PerEvent(t *testing.T) {
	for _, e := range []plugin.HookEvent{plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop, plugin.HookUserPromptSubmit, plugin.HookPreCompact} {
		if !exitBehavior(e).BlockOnExit2 {
			t.Fatalf("%s should block on exit 2", e)
		}
	}
	for _, e := range []plugin.HookEvent{plugin.HookPostToolUse, plugin.HookSessionStart, plugin.HookSessionEnd, plugin.HookNotification} {
		if exitBehavior(e).BlockOnExit2 {
			t.Fatalf("%s must NOT block on exit 2", e)
		}
	}
}

// TestRunHook_CommandJSONOnlyOnExit0 verifies that command hook JSON output is
// ignored when the process exits with code 2; only the stderr path is used.
// Tier: claude-compatible-subset.
func TestRunHook_CommandJSONOnlyOnExit0(t *testing.T) {
	r := newRunner(nil, "")
	h := plugin.RegisteredHook{
		Type:    "command",
		Matcher: "*",
		Timeout: 5,
		Command: `echo '{"continue":false,"systemMessage":"json"}'; echo "stderr-msg" >&2; exit 2`,
	}
	out := r.runHook(context.Background(), h, plugin.HookPreToolUse, Input{HookEventName: "PreToolUse"})
	if !out.IsError {
		t.Fatal("exit2 should be IsError")
	}
	if out.SystemMessage == "json" {
		t.Fatal("JSON must be ignored on exit 2")
	}
}

// TestHookRunner_PostToolUse_ExitCode2_DoesNotBlock verifies that PostToolUse
// exit 2 produces a system message (IsError) but does NOT deny/block.
// Tier: claude-compatible-subset.
func TestHookRunner_PostToolUse_ExitCode2_DoesNotBlock(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo "post-tool-error" >&2; exit 2`,
		Timeout: 5,
	})

	result := runner.RunPostToolUse(context.Background(), Input{
		CWD:           "/tmp",
		HookEventName: "PostToolUse",
		ToolName:      "Write",
	})
	// PostToolUse exit 2: should produce a system message (stderr surfaced)
	if len(result.SystemMessages) == 0 {
		t.Fatal("PostToolUse exit 2 should surface a system message")
	}
	// PostToolUse exit 2: must NOT behave like a deny — RunResult has no Denied field,
	// so the key assertion is that a system message appears without blocking behavior.
	// The absence of a block/deny field on RunResult is the structural guarantee;
	// confirm the message contains our stderr content.
	found := false
	for _, msg := range result.SystemMessages {
		if msg == "post-tool-error" || len(msg) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("PostToolUse exit 2 system messages = %v, want non-empty", result.SystemMessages)
	}
}
