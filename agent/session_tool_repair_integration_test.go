package agent

import (
	"context"
	"encoding/json"
	"strings"
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

// TestExecTool_InvalidJSONWithValidLeadingMemberDivergesIntent is a regression
// guard for finding A direction 1: invalid JSON with a valid leading member (a
// missing comma after "intent"). Go's json.Unmarshal discards the map on any
// syntax error (no partial population), so live does NOT show intent today --
// which already agrees with reload (RawArguments is set, intent skipped). The
// json.Valid gate the fix adds preserves this: it skips the unmarshal of
// known-invalid bytes entirely, keeping the two paths semantically identical.
func TestExecTool_InvalidJSONWithValidLeadingMemberDivergesIntent(t *testing.T) {
	// Missing comma between members: {"intent":"foo" "bar":"baz"}.
	// json.Unmarshal stores "intent" then errors at the missing separator.
	const originalArgs = `{"intent":"foo" "bar":"baz"}`
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	s.RegisterTool("widget", "does a thing", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"intent": map[string]any{"type": "string"},
			"bar":    map[string]any{"type": "string"},
		},
		"required": []any{"intent"},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	startCh := drainToolCallStartEvents(s)

	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_invalid_leading",
		Name:      "widget",
		Arguments: json.RawMessage(originalArgs),
	}, "")
	// The call is correctly rejected (invalid JSON is not repairable), but
	// execTool still emits a ToolCallStart event before the PrevalErr dispatch.
	// The divergence is in that start event's Description (intent).
	_ = res
	s.Close()

	starts := <-startCh
	if len(starts) != 1 {
		t.Fatalf("got %d ToolCallStart events, want 1", len(starts))
	}
	// Reload keys intent off RawArguments != "" (set only when !json.Valid).
	// These bytes are not valid JSON, so RawArguments is set on the durable
	// record and reload shows NO intent. The live path must agree: a partial
	// unmarshal that happens to populate "intent" before the syntax error
	// must NOT surface it.
	if starts[0].Description != "" {
		t.Fatalf("Description = %q, want empty: invalid-JSON original must not show intent (reload shows none when RawArguments is set)", starts[0].Description)
	}
}

// TestExecTool_OversizedValidJSONSuppressesIntent proves finding 4: oversized
// VALID JSON is rejected by ValidateRawArguments (byte length), so the live path
// suppresses intent (Description=""). The reload side now applies the same
// size gate via tool.ValidateRawArguments in the projection (see
// TestProjectTurn_OversizedValidJSONSuppressesIntent) and in the markdown
// rendering (see TestRenderMarkdown_OversizedValidJSONSuppressesIntent), so
// both paths suppress intent consistently. This test pins the live safety
// suppression (no intent) that the fix preserves.
func TestExecTool_OversizedValidJSONSuppressesIntent(t *testing.T) {
	// Valid JSON object but over the 2 MiB MaxToolArgumentBytes limit.
	large := strings.Repeat("x", 2*1024*1024+10)
	originalArgs := []byte(`{"intent":"a","bar":"` + large + `"}`)

	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	s.RegisterTool("widget", "does a thing", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"intent": map[string]any{"type": "string"},
			"bar":    map[string]any{"type": "string"},
		},
		"required": []any{"intent"},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	startCh := drainToolCallStartEvents(s)

	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_oversized",
		Name:      "widget",
		Arguments: json.RawMessage(originalArgs),
	}, "")
	// Oversized bytes are rejected pre-validation; execTool returns an error result.
	if !res.IsError {
		t.Fatalf("expected oversized args to be rejected, got success: %s", res.FullOutput)
	}
	s.Close()

	starts := <-startCh
	if len(starts) != 1 {
		t.Fatalf("got %d ToolCallStart events, want 1", len(starts))
	}
	// Live suppresses intent for rejected bytes (RawArgumentsRejected). The
	// finding notes reload diverges (RawArguments == "" for oversized valid
	// JSON, so reload shows intent). This assertion pins the live suppression
	// the fix must preserve.
	if starts[0].Description != "" {
		t.Fatalf("Description = %q, want empty: RawArgumentsRejected must suppress intent", starts[0].Description)
	}
}

// TestExecTool_ValidNoncanonicalArgsCanonicalizeLikeTranscript proves finding B:
// VALID but noncanonical arguments (extra whitespace, HTML-sensitive chars in a
// string value) must render the same live as reload. The transcript persistence
// path compacts and HTML-escapes valid JSON (json.Marshal of json.RawMessage),
// so the live ArgumentsJSON must too -- emitting raw original bytes diverges.
func TestExecTool_ValidNoncanonicalArgsCanonicalizeLikeTranscript(t *testing.T) {
	// Valid JSON with extra whitespace and HTML-sensitive chars: < > &.
	const originalArgs = `{  "intent" : "b<c & d" , "x" : 1  }`
	// The canonical form is compact + HTML-escaped, exactly what
	// json.Marshal(json.RawMessage(originalArgs)) produces.
	canonical, err := json.Marshal(json.RawMessage(originalArgs))
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}

	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	s.RegisterTool("widget", "does a thing", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"intent": map[string]any{"type": "string"},
			"x":      map[string]any{"type": "number"},
		},
		"required": []any{"intent"},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	startCh := drainToolCallStartEvents(s)

	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "call_noncanonical",
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
	if starts[0].ArgumentsJSON != string(canonical) {
		t.Fatalf("ArgumentsJSON = %q\nwant canonical (matches reload transcript): %q", starts[0].ArgumentsJSON, string(canonical))
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
