package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// These tests prove the `continue: false` contract at each dispatch site that
// consumes it: the hook's stopReason becomes the user-visible (or model-facing)
// message AND the action the event guards actually stops. Each uses a real
// `bash -c` hook echoing JSON on stdout, and asserts the real effect — a tool
// that never ran, a turn that ended — never the hook script's raw output.

// TestIntg_ExecTool_PreToolUseContinueFalseStopsTheTool proves a PreToolUse
// continue:false stops the call: the tool never runs and the model receives the
// hook's stopReason as the call's error result.
func TestIntg_ExecTool_PreToolUseContinueFalseStopsTheTool(t *testing.T) {
	t.Parallel()
	sess, ran := intg_hookSession(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo '{\"continue\":false,\"stopReason\":\"stopped-by-intg-hook\"}'"}]}
			]
		}
	}`)
	collect := drainEvents(sess)

	res := sess.execTool(context.Background(), intg_probeCall(`{"path":"original"}`), "")

	if !res.IsError {
		t.Fatalf("a continue:false tool call should be an error result; got %+v", res)
	}
	if res.Output != "stopped-by-intg-hook" {
		t.Errorf("stop message = %q, want %q", res.Output, "stopped-by-intg-hook")
	}
	if got := ran.Load(); got != 0 {
		t.Errorf("probe tool ran %d time(s) despite continue:false; want 0", got)
	}
	sess.Close()
	collect()
}

// postHookFixture builds a session whose plugin declares one command hook on the
// named event, answered by a scripted adapter so a test can see exactly how many
// model requests happened.
func postHookFixture(t *testing.T, event, command string, respond func(round int, req llm.Request) llm.Response) (*Session, *choiceAdapter) {
	t.Helper()
	hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{event: []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}},
	}}})
	if err != nil {
		t.Fatalf("encode hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "continue-false-plugin", string(hooksJSON))
	adapter := newChoiceAdapter("openai", respond)
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:   t.TempDir(),
		PluginDirs: []string{pluginDir},
		testOnly:   testConfig{skipGitSnapshot: true},
	}))
	if sess.hookRunner == nil {
		t.Fatalf("test setup: the plugin's %s hook did not reach a hook runner", event)
	}
	return sess, adapter
}

// TestPostToolUse_ContinueFalseEndsTheTurnAfterPersisting proves a PostToolUse
// continue:false ends the turn after the round's tool results are already
// durable: one model request total, the reason delivered as a user warning and a
// steering turn, and the tool result still in history.
func TestPostToolUse_ContinueFalseEndsTheTurnAfterPersisting(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"post-tool-use stopped the turn"}'`
	sess, adapter := postHookFixture(t, "PostToolUse", cmd, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("the tool hook's continue:false must end the turn; reached request %d", round)
		}
		return toolCallResponse(batchShellCall("shell-0"))
	})
	warnings := collectWarnings(sess)

	// TRIPWIRE: a scripted in-process adapter, so only a genuine hang reaches 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run a tool", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "post-tool-use stopped the turn")
	sess.Close()

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: continue:false stops the round loop", got)
	}
	if _, ok := findToolResultInHistory(sess.history, "shell-0"); !ok {
		t.Fatal("the round's tool result is missing from history: results are persisted before the stop")
	}
	var steering []schema.Turn
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindHookContext {
			steering = append(steering, turn)
		}
	}
	if len(steering) != 1 || !strings.Contains(steering[0].Message.Text(), "post-tool-use stopped the turn") {
		t.Fatalf("hook-context steering turns = %+v, want one carrying the stop reason", steering)
	}
}

// TestPostToolUseFailure_ContinueFalseEndsTheTurn proves the failure half of the
// same partition honors continue:false through the one consumer.
func TestPostToolUseFailure_ContinueFalseEndsTheTurn(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"failure hook stopped the turn"}'`
	// A read of a path that does not exist runs and fails, so it is
	// PostToolUseFailure's case; an unknown tool would be rejected before
	// execution and fire neither tool event.
	failCall := llm.ToolCallData{
		ID:        "fail-0",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":"/nonexistent/continue-false-probe"}`),
		Type:      "function",
	}
	sess, adapter := postHookFixture(t, "PostToolUseFailure", cmd, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("the hook's continue:false must end the turn; reached request %d", round)
		}
		return toolCallResponse(failCall)
	})
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run a failing tool", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "failure hook stopped the turn")
	sess.Close()

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: continue:false stops the round loop", got)
	}
}

// TestStop_ContinueFalseEndsTheTurnWithItsReason proves a Stop hook's
// continue:false ends the turn and its stopReason becomes the delivered text,
// replacing the reply the hook vetoed.
func TestStop_ContinueFalseEndsTheTurnWithItsReason(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"stop hook ended the turn"}'`
	sess, adapter := postHookFixture(t, "Stop", cmd, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("the Stop hook's continue:false must end the turn; reached request %d", round)
		}
		return communicateResponse(true, "the hook will veto this reply")
	})
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	text, err := sess.ProcessInput(ctx, "answer me", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "stop hook ended the turn")
	sess.Close()

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: the stop ends the turn", got)
	}
	if text != "stop hook ended the turn" {
		t.Errorf("delivered text = %q, want the hook's stopReason", text)
	}
}

// TestSubagentStop_ContinueFalseEndsTheRunWithNoContinuation proves a
// SubagentStop continue:false ends the child run instead of starting the
// continuation a block would: the stop reason becomes the run's result and no
// model request follows.
func TestSubagentStop_ContinueFalseEndsTheRunWithNoContinuation(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	subSess := newSession(t, withSteps(func(llm.Request) llm.Response {
		requests.Add(1)
		return finalResponse("ok")
	}))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s' '{"continue":false,"stopReason":"subagent stop ended the run"}'`,
		Timeout: 5,
	})
	subSess.hookRunner = runner
	sub := &subagent{id: subSess.id, sess: subSess, status: SubagentRunning}
	got, err := sub.runSubagentStopHook(context.Background(), "the delivered report", nil, nil)
	if err != nil {
		t.Fatalf("runSubagentStopHook: %v", err)
	}
	subSess.Close()

	if got != "subagent stop ended the run" {
		t.Errorf("run result = %q, want the hook's stopReason", got)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("model requests = %d, want 0: a stop starts no continuation", n)
	}
}

// TestPostToolUse_ContinueFalseWithNoReasonStillEndsTheTurn proves the stop is
// keyed on the FACT, not the reason. `{"continue":false}` with no stopReason is
// legal Claude; a bare-string carrier would read it as "not stopped" and let the
// loop reach another model request. It must end the turn with a default message.
func TestPostToolUse_ContinueFalseWithNoReasonStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false}'`
	sess, adapter := postHookFixture(t, "PostToolUse", cmd, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("a reasonless continue:false must still end the turn; reached request %d", round)
		}
		return toolCallResponse(batchShellCall("shell-0"))
	})
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run a tool", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "a tool hook stopped this round")
	sess.Close()

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: a reasonless continue:false still stops the loop", got)
	}
}

// TestSessionStart_ContinueFalseDoesNotLeakPastAFailedAdmission proves a stop
// recorded for an input that never ran does not swallow the next prompt. The
// first prompt fails before its user turn is recorded; the second prompt is an
// ordinary turn, must reach the model, and must get the model's answer.
func TestSessionStart_ContinueFalseDoesNotLeakPastAFailedAdmission(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"startup hook stopped the session"}'`
	sess, adapter := postHookFixture(t, "SessionStart", cmd, func(_ int, _ llm.Request) llm.Response {
		return finalResponse("second prompt answered")
	})
	// Fail exactly the first input, before its user turn is recorded. The seam
	// is the pre-incorporation failure a SessionStart stop can be recorded
	// across; nil in production.
	sess.cfg.testOnly.failTurnBeforeRecording = func() error {
		sess.cfg.testOnly.failTurnBeforeRecording = nil
		return errors.New("first prompt refused")
	}
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "first prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "first prompt refused") {
		t.Fatalf("first prompt error = %v, want the injected refusal", err)
	}
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model requests after the failed admission = %d, want 0", got)
	}
	awaitWarningNaming(t, warnings, "startup hook stopped the session")

	text, err := sess.ProcessInput(ctx, "second prompt", nil)
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	sess.Close()

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: a stale SessionStart stop swallowed the second prompt", got)
	}
	if text != "second prompt answered" {
		t.Fatalf("second prompt's answer = %q, want the model's reply: the stop leaked onto it", text)
	}
}

// TestSubagentStop_ContinueFalseKeepsTheRunError proves a stop ends the run
// without laundering a failure into success: a child whose run errored still
// reports its error, even though the hook's stop supplies the result text.
func TestSubagentStop_ContinueFalseKeepsTheRunError(t *testing.T) {
	t.Parallel()
	subSess := newSession(t, withSteps(func(llm.Request) llm.Response { return finalResponse("ok") }))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `printf '%s' '{"continue":false,"stopReason":"subagent stop ended the run"}'`,
		Timeout: 5,
	})
	subSess.hookRunner = runner
	sub := &subagent{id: subSess.id, sess: subSess, status: SubagentRunning}
	runErr := errors.New("provider exploded")

	got, err := sub.runSubagentStopHook(context.Background(), "the delivered report", runErr, nil)
	subSess.Close()

	if got != "subagent stop ended the run" {
		t.Errorf("run result = %q, want the hook's stopReason", got)
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("run error = %v, want the original run error: a stop must not report a failed child as completed", err)
	}
}

// TestUserPromptSubmit_ContinueFalseSkipsTheModelCall proves a
// UserPromptSubmit continue:false ends the turn with zero model requests: the
// prompt never reaches the model, and its reason is delivered to the user.
func TestUserPromptSubmit_ContinueFalseSkipsTheModelCall(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"prompt hook refused the prompt"}'`
	sess, adapter := postHookFixture(t, "UserPromptSubmit", cmd, func(round int, req llm.Request) llm.Response {
		t.Fatalf("a stopped prompt must reach no model request; reached request %d", round)
		return llm.Response{}
	})
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "a prompt the hook refuses", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "prompt hook refused the prompt")
	sess.Close()

	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model requests = %d, want 0: continue:false keeps the prompt from the model", got)
	}
}

// TestSessionStart_ContinueFalseSkipsTheFirstTurn proves a startup SessionStart
// continue:false ends the session's first turn before its model request: the
// reason is delivered to the user and the model is never called.
func TestSessionStart_ContinueFalseSkipsTheFirstTurn(t *testing.T) {
	t.Parallel()
	cmd := `printf '%s' '{"continue":false,"stopReason":"startup hook stopped the session"}'`
	sess, adapter := postHookFixture(t, "SessionStart", cmd, func(round int, req llm.Request) llm.Response {
		t.Fatalf("a stopped session must reach no model request; reached request %d", round)
		return llm.Response{}
	})
	warnings := collectWarnings(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first prompt", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	awaitWarningNaming(t, warnings, "startup hook stopped the session")
	sess.Close()

	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model requests = %d, want 0: continue:false stops the first turn", got)
	}
}
