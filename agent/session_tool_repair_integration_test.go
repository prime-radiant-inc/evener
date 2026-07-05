package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
