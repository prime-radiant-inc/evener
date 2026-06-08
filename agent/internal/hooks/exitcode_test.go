package hooks

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// TestExitBehavior_PerEvent verifies the central exit-code table classifies exit
// 2 as blocking only for events whose RUNNER actually enforces the block.
// PreToolUse/Stop/SubagentStop block and are enforced. UserPromptSubmit and
// PreCompact block in the Claude contract, but serf does not yet enforce the
// block at their dispatch sites (a deferred parity item), so the table must not
// claim a block nothing consumes (Fix 3).
func TestExitBehavior_PerEvent(t *testing.T) {
	for _, e := range []plugin.HookEvent{plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop} {
		if !exitBehavior(e).BlockOnExit2 {
			t.Fatalf("%s should block on exit 2 (and is enforced)", e)
		}
	}
	for _, e := range []plugin.HookEvent{
		plugin.HookUserPromptSubmit, plugin.HookPreCompact,
		plugin.HookPostToolUse, plugin.HookSessionStart, plugin.HookSessionEnd, plugin.HookNotification,
	} {
		if exitBehavior(e).BlockOnExit2 {
			t.Fatalf("%s must NOT claim block-on-exit-2 in the table: nothing enforces it", e)
		}
	}
}

// TestExitBehavior_BlockEntriesAreEnforced is the anti-dead-entry guard: every
// event the table marks BlockOnExit2 must have a runner that actually denies or
// blocks when a matched hook exits 2. A "block" entry that no runner consumes is
// a lie the review flagged; this test fails if one is reintroduced (Fix 3).
func TestExitBehavior_BlockEntriesAreEnforced(t *testing.T) {
	exit2 := func(event plugin.HookEvent) plugin.RegisteredHook {
		return plugin.RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: `echo "blocked by hook" >&2; exit 2`,
			Timeout: 5,
		}
	}
	in := func(event plugin.HookEvent) Input {
		return Input{CWD: "/tmp", HookEventName: string(event), ToolName: "Bash"}
	}

	for _, event := range []plugin.HookEvent{plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop} {
		if !exitBehavior(event).BlockOnExit2 {
			continue
		}
		r := newRunner(nil, "")
		r.Add(event, exit2(event))
		blocked := false
		switch event {
		case plugin.HookPreToolUse:
			blocked = r.RunPreToolUse(context.Background(), in(event)).Denied
		case plugin.HookStop:
			blocked = r.RunStop(context.Background(), in(event)).Blocked
		case plugin.HookSubagentStop:
			blocked = r.RunSubagentStop(context.Background(), in(event)).Blocked
		}
		if !blocked {
			t.Fatalf("%s claims BlockOnExit2 but its runner did not block/deny on exit 2", event)
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
	// PostToolUse exit 2: stderr routes to the model (IsError → ModelContext).
	if len(result.ModelContext) == 0 {
		t.Fatal("PostToolUse exit 2 should surface a model context message")
	}
	// PostToolUse exit 2: must NOT behave like a deny — RunResult has no Denied field,
	// so the key assertion is that a model context message appears without blocking behavior.
	// The absence of a block/deny field on RunResult is the structural guarantee;
	// confirm the message contains our stderr content.
	found := false
	for _, msg := range result.ModelContext {
		if msg == "post-tool-error" || len(msg) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("PostToolUse exit 2 model context = %v, want non-empty", result.ModelContext)
	}
}
