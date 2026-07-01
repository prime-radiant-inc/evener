package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

// These tests drive Session.execTool with real command hooks configured on the
// session so the PreToolUse / PostToolUse decision arms (deny, updatedInput
// rewrite, model-context and user-message delivery) actually fire. Each hook is
// a real `bash -c` subprocess that echoes a JSON decision on stdout; assertions
// check the REAL effect on the session (tool blocked, arguments rewritten,
// steering queue, diagnostic warning) — never the hook script's raw output.

// intg_hookSession builds a session that loads a plugin whose hooks.json holds
// the given contents, and registers a probe tool that records whether it ran and
// echoes back its received "path" argument. The returned atomic reports the
// probe's invocation count.
func intg_hookSession(t *testing.T, hooksJSON string) (*Session, *atomic.Int32) {
	t.Helper()
	dir := writePluginHooks(t, "intg-hook-plugin", hooksJSON)

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var ran atomic.Int32
	sess.RegisterTool("intg_probe", "records that it ran and echoes its path arg",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		},
		func(_ context.Context, args any) (any, error) {
			ran.Add(1)
			if m, ok := args.(map[string]any); ok {
				if p, ok := m["path"].(string); ok {
					return "path=" + p, nil
				}
			}
			return "path=", nil
		})
	return sess, &ran
}

// intg_probeCall builds a probe tool call with the given raw JSON arguments.
func intg_probeCall(args string) llm.ToolCallData {
	return llm.ToolCallData{ID: "call_intg", Name: "intg_probe", Arguments: json.RawMessage(args)}
}

func TestIntg_ExecTool_PreToolUseDeniesTool(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"blocked-by-intg-hook\"}}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`))

	if !res.IsError {
		t.Fatalf("denied tool call should be an error result; got %+v", res)
	}
	if res.Output != "blocked-by-intg-hook" {
		t.Errorf("deny message = %q, want %q", res.Output, "blocked-by-intg-hook")
	}
	if got := ran.Load(); got != 0 {
		t.Errorf("probe tool ran %d time(s) despite a deny; want 0", got)
	}
	sess.Close()
	collect()
}

func TestIntg_ExecTool_PreToolUseRewritesInput(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"updatedInput\":{\"path\":\"rewritten\"}}}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`))

	if res.IsError {
		t.Fatalf("allowed tool call should not error; got %+v", res)
	}
	if got := ran.Load(); got != 1 {
		t.Fatalf("probe tool ran %d time(s); want 1", got)
	}
	// The hook's updatedInput must have replaced the original argument before the
	// tool executed.
	if res.Output != "path=rewritten" {
		t.Errorf("tool saw arguments %q, want %q (updatedInput rewrite lost)", res.Output, "path=rewritten")
	}
	sess.Close()
	collect()
}

func TestIntg_ExecTool_PreToolUseInvalidUpdatedInputErrors(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"updatedInput\":{\"path\":\"rewritten\"}}}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	// Existing arguments are malformed JSON, so merging the hook's updatedInput
	// into them fails and the tool call is rejected before it runs.
	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"`))

	if !res.IsError {
		t.Fatalf("malformed-args + updatedInput should error; got %+v", res)
	}
	if !strings.HasPrefix(res.Output, "invalid hook updatedInput") {
		t.Errorf("error output = %q, want it to start with %q", res.Output, "invalid hook updatedInput")
	}
	if got := ran.Load(); got != 0 {
		t.Errorf("probe tool ran %d time(s) despite invalid updatedInput; want 0", got)
	}
	sess.Close()
	collect()
}

func TestIntg_ExecTool_PreToolUseDeliversModelContext(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"additionalContext\":\"pre-context-note\"}}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`))
	if res.IsError {
		t.Fatalf("tool should run; got %+v", res)
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("probe tool ran %d time(s); want 1", got)
	}
	// PreToolUse additionalContext is delivered to the model as a steering turn.
	steer := sess.SteeringQueueSnapshot()
	if !intg_steeringHas(steer, "<SYSTEM-REMINDER>pre-context-note</SYSTEM-REMINDER>") {
		t.Errorf("PreToolUse additionalContext not delivered as steering; got %+v", steer)
	}
	sess.Close()
	collect()
}

func TestIntg_ExecTool_PreToolUseDeliversUserMessage(t *testing.T) {
	t.Parallel()
	sess, _ := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"systemMessage\":\"pre-user-note\"}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`))
	sess.Close()
	evs := collect()

	// PreToolUse systemMessage surfaces as a hook-sourced diagnostic warning.
	if !intg_hasHookWarning(evs, "pre-user-note") {
		t.Errorf("PreToolUse systemMessage not surfaced as a hook diagnostic warning; events=%d", len(evs))
	}
}

func TestIntg_ExecTool_PostToolUseDeliversContextAndUserMessage(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PostToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"systemMessage\":\"post-user-note\",\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"additionalContext\":\"post-context-note\"}}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`))
	if res.IsError {
		t.Fatalf("tool should run; got %+v", res)
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("probe tool ran %d time(s); want 1", got)
	}
	// PostToolUse additionalContext -> model steering.
	steer := sess.SteeringQueueSnapshot()
	if !intg_steeringHas(steer, "<SYSTEM-REMINDER>post-context-note</SYSTEM-REMINDER>") {
		t.Errorf("PostToolUse additionalContext not delivered as steering; got %+v", steer)
	}
	sess.Close()
	evs := collect()
	// PostToolUse systemMessage -> hook diagnostic warning.
	if !intg_hasHookWarning(evs, "post-user-note") {
		t.Errorf("PostToolUse systemMessage not surfaced as a hook diagnostic warning; events=%d", len(evs))
	}
}

func intg_steeringHas(entries []SteeringEntry, text string) bool {
	for _, e := range entries {
		if e.Text == text {
			return true
		}
	}
	return false
}

func intg_hasHookWarning(evs []events.SessionEvent, msg string) bool {
	for _, ev := range evs {
		if w, ok := ev.Data.(events.WarningData); ok && w.Source == string(diagnostic.SourceHook) && w.Message == msg {
			return true
		}
	}
	return false
}
