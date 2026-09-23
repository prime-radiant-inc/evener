package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// widgetSchema is a custom tool schema with additionalProperties:false so a
// model call using the "path" alias (instead of the declared "file_path")
// fails schema validation and must go through repair before it can execute.
func widgetSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
		"required": []any{"file_path"},
	}
}

// drainRepairedEvents collects EventToolCallRepaired payloads from a session's
// event stream until it closes (i.e. after sess.Close()).
func drainRepairedEvents(sess *Session) <-chan []events.ToolCallRepairedData {
	out := make(chan []events.ToolCallRepairedData, 1)
	go func() {
		var repaired []events.ToolCallRepairedData
		for ev := range sess.Events() {
			if d, ok := ev.Data.(events.ToolCallRepairedData); ok {
				repaired = append(repaired, d)
			}
		}
		out <- repaired
	}()
	return out
}

// drainToolCallStartEvents collects ToolCallStart payloads from a session's
// event stream until it closes (i.e. after sess.Close()).
func drainToolCallStartEvents(sess *Session) <-chan []events.ToolCallStartData {
	out := make(chan []events.ToolCallStartData, 1)
	go func() {
		var starts []events.ToolCallStartData
		for ev := range sess.Events() {
			if d, ok := ev.Data.(events.ToolCallStartData); ok {
				starts = append(starts, d)
			}
		}
		out <- starts
	}()
	return out
}

// TestExecTool_RepairableMalformedArgsPreserveOriginalInEvents proves that when
// prepareToolCall repairs malformed (but repairable) tool arguments, the live
// tool-call events carry the model's ORIGINAL argument bytes, not the repaired
// form -- so live display matches reload (SentArguments). The Description (intent)
// must also be empty for invalid-JSON originals, matching reload which skips
// intent extraction when RawArguments is set.
func TestExecTool_RepairableMalformedArgsPreserveOriginalInEvents(t *testing.T) {
	const originalArgs = `{file_path: "/x", intent: "editing"}` // bare keys -- repairable
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	s.RegisterTool("widget", "does a thing", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"intent":    map[string]any{"type": "string"},
		},
		"required": []any{"file_path"},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	startCh := drainToolCallStartEvents(s)

	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_repairable",
		Name:      "widget",
		Arguments: json.RawMessage(originalArgs),
	}, "")
	if res.IsError {
		t.Fatalf("execTool failed: %s", res.FullOutput)
	}
	s.Close()

	starts := <-startCh
	if len(starts) != 1 {
		t.Fatalf("got %d ToolCallStart events, want 1", len(starts))
	}
	if starts[0].ArgumentsJSON != originalArgs {
		t.Fatalf("ArgumentsJSON = %q, want original %q (live must match reload SentArguments)", starts[0].ArgumentsJSON, originalArgs)
	}
	if starts[0].Description != "" {
		t.Fatalf("Description = %q, want empty for invalid-JSON-original call (reload skips intent when RawArguments is set)", starts[0].Description)
	}
}

// TestSession_RepairsAliasedArgAndEmitsEvent drives a full ProcessInput round
// where the fake model calls a custom tool using an off-distribution alias
// ("path" instead of the declared "file_path"). execTool must heal the args
// before dispatch (so the executor sees "file_path", not "path") and must
// emit EventToolCallRepaired recording the change.
func TestSession_RepairsAliasedArgAndEmitsEvent(t *testing.T) {
	aliasedCall := llm.ToolCallData{ID: "call1", Name: "widget", Arguments: json.RawMessage(`{"path":"/x"}`)}
	comm := communicateCall("c1", "done")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(aliasedCall) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	sess := newSession(t, withAdapter(f))

	var gotArgs map[string]any
	sess.RegisterTool("widget", "does a thing", widgetSchema(), func(ctx context.Context, args any) (any, error) {
		gotArgs, _ = args.(map[string]any)
		return "done", nil
	})

	repairedCh := drainRepairedEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "call widget", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	repaired := <-repairedCh

	if gotArgs == nil {
		t.Fatal("tool executor never ran")
	}
	if gotArgs["file_path"] != "/x" {
		t.Fatalf("tool did not receive healed file_path: %v", gotArgs)
	}
	if _, ok := gotArgs["path"]; ok {
		t.Fatalf("unhealed 'path' key reached the tool: %v", gotArgs)
	}
	if len(repaired) == 0 || repaired[0].ToolName != "widget" {
		t.Fatalf("EventToolCallRepaired not emitted: %+v", repaired)
	}
	if len(repaired[0].Changes) == 0 {
		t.Fatalf("expected repair changes recorded: %+v", repaired[0])
	}
}

// TestSession_RepairedThenDeniedCallStillEmitsRepairedEvent covers the
// telemetry guard: EventToolCallRepaired must fire even when a PreToolUse hook
// denies the (already-healed) call, since repair happens before the hook
// block runs.
func TestSession_RepairedThenDeniedCallStillEmitsRepairedEvent(t *testing.T) {
	aliasedCall := llm.ToolCallData{ID: "call1", Name: "widget", Arguments: json.RawMessage(`{"path":"/x"}`)}
	comm := communicateCall("c1", "done")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(aliasedCall) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	sess := newSession(t, withAdapter(f))

	var toolRan bool
	sess.RegisterTool("widget", "does a thing", widgetSchema(), func(ctx context.Context, args any) (any, error) {
		toolRan = true
		return "done", nil
	})

	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "widget",
		Type:    "command",
		Command: `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"denied for test"}}'`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	repairedCh := drainRepairedEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "call widget", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	repaired := <-repairedCh

	if toolRan {
		t.Fatal("denied tool call must not execute")
	}
	if len(repaired) == 0 || repaired[0].ToolName != "widget" {
		t.Fatalf("EventToolCallRepaired not emitted for repaired-then-denied call: %+v", repaired)
	}
}
