package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// decodeHookCapture reads every JSON line a hook capture file holds, in order.
func decodeHookCapture(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read hook capture %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode hook input %q: %v", line, err)
		}
		out = append(out, decoded)
	}
	return out
}

// appendStdinHook returns a command that copies its stdin (the hook input JSON)
// to path, one JSON object per line.
func appendStdinHook(path string) string {
	return "cat >> " + shellquote.Literal(path) + "; echo >> " + shellquote.Literal(path)
}

// TestStop_HookInputFields proves Stop carries stop_hook_active (false first,
// true after a block) and last_assistant_message equal to the delivered reply.
func TestStop_HookInputFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	capture := filepath.Join(dir, "stop-input.jsonl")
	readTarget := filepath.Join(dir, "read-me.txt")
	if err := os.WriteFile(readTarget, []byte("contents"), 0o644); err != nil {
		t.Fatalf("write read target: %v", err)
	}
	// The hook records every dispatch and blocks, so the turn never finishes and
	// the next explicit dispatch in the test is a continuation of the same turn.
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: appendStdinHook(capture) + `; printf '%s' '{"decision":"block","reason":"keep going"}'`,
	})
	sess := newSession(t, withDir(dir))
	sess.hookRunner = runner
	drainSessionEvents(sess)

	dispatch := func(reply string) bool {
		t.Helper()
		sess.mu.Lock()
		sess.state = SessionProcessing
		sess.comm = communicateResult{called: true, reply: reply}
		sess.mu.Unlock()
		done, text, err := sess.deliverIfCommunicated(context.Background(), false)
		if err != nil {
			t.Fatalf("deliverIfCommunicated(%q): %v", reply, err)
		}
		if done {
			t.Fatalf("a blocking Stop hook must not finish the turn (reply %q)", reply)
		}
		if text != "" {
			t.Fatalf("a blocked boundary must return no reply, got %q", text)
		}
		return done
	}

	dispatch("first reply")
	dispatch("second reply")

	// A tool call clears the flag: the continuation a Stop block started is over.
	if res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "stop-flag-clear",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":` + strconvQuote(readTarget) + `}`),
	}, ""); res.IsError {
		t.Fatalf("read_file failed: %s", res.FullOutput)
	}
	dispatch("third reply")

	captured := decodeHookCapture(t, capture)
	if len(captured) != 3 {
		t.Fatalf("Stop hook ran %d time(s), want 3: %+v", len(captured), captured)
	}
	wantActive := []bool{false, true, false}
	wantMessage := []string{"first reply", "second reply", "third reply"}
	for i, got := range captured {
		active, present := got["stop_hook_active"].(bool)
		if !present || active != wantActive[i] {
			t.Errorf("dispatch %d stop_hook_active = %v (present=%v), want %v", i, got["stop_hook_active"], present, wantActive[i])
		}
		if msg, _ := got["last_assistant_message"].(string); msg != wantMessage[i] {
			t.Errorf("dispatch %d last_assistant_message = %q, want %q", i, msg, wantMessage[i])
		}
	}
}

// strconvQuote renders s as a JSON string literal for RawMessage arguments.
func strconvQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestSubagentStop_MatchesAgentTypeAndCarriesClosingText proves the agent-type
// matcher and that last_assistant_message is the child's last assistant turn,
// not the delivered report.
func TestSubagentStop_MatchesAgentTypeAndCarriesClosingText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	capture := filepath.Join(dir, "subagent-stop-input.jsonl")
	agentMatched := filepath.Join(dir, "subagent-matched")
	otherMatched := filepath.Join(dir, "other-matched")

	child := newSession(t, withSteps(func(llm.Request) llm.Response { return finalResponse("ok") }))
	child.mu.Lock()
	child.history = append(child.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("the child's closing text")))
	child.mu.Unlock()

	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "subagent", Type: "command", Timeout: 5,
		Command: appendStdinHook(capture) + "; touch " + shellquote.Literal(agentMatched),
	})
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "other-agent", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(otherMatched),
	})
	child.hookRunner = runner

	sub := &subagent{id: child.id, sess: child, status: SubagentRunning}
	res, err := sub.runSubagentStopHook(context.Background(), "the delivered report", nil, nil)
	if err != nil {
		t.Fatalf("runSubagentStopHook: %v", err)
	}
	if res != "the delivered report" {
		t.Fatalf("result = %q, want the delivered report unchanged", res)
	}

	if _, err := os.Stat(agentMatched); err != nil {
		t.Fatalf("an agent_type-matched SubagentStop hook did not fire: %v", err)
	}
	if _, err := os.Stat(otherMatched); !os.IsNotExist(err) {
		t.Fatalf("a hook matched on another agent type fired; stat err = %v", err)
	}

	captured := decodeHookCapture(t, capture)
	if len(captured) != 1 {
		t.Fatalf("SubagentStop hook ran %d time(s), want 1", len(captured))
	}
	got := captured[0]
	if got["agent_type"] != "subagent" {
		t.Errorf("agent_type = %v, want subagent", got["agent_type"])
	}
	if msg, _ := got["last_assistant_message"].(string); msg != "the child's closing text" {
		t.Errorf("last_assistant_message = %q, want the child's last assistant turn", msg)
	}
	active, present := got["stop_hook_active"].(bool)
	if !present || active {
		t.Errorf("stop_hook_active = %v (present=%v), want false at the run's first dispatch", got["stop_hook_active"], present)
	}
}

// TestSessionEnd_ReasonVocabulary proves a clear close sends "clear" and a plain
// close sends "other", and a hook matcher for each fires only on its own.
func TestSessionEnd_ReasonVocabulary(t *testing.T) {
	t.Parallel()

	// closeWith builds a session carrying one SessionEnd hook per reason, each
	// writing its own marker and stdin capture, and returns what fired.
	closeWith := func(reason string, close func(*Session)) (clearFired, otherFired bool, captured []map[string]any) {
		dir := t.TempDir()
		capture := filepath.Join(dir, "session-end-input.jsonl")
		clearMarker := filepath.Join(dir, "clear-matched")
		otherMarker := filepath.Join(dir, "other-matched")
		runner := hooks.NewRunner(nil, "")
		// The capture hook matches every reason so the test can read the reason
		// each close actually sent; the two markers prove the matcher itself.
		runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
			Matcher: "*", Type: "command", Timeout: 5,
			Command: appendStdinHook(capture),
		})
		runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
			Matcher: "clear", Type: "command", Timeout: 5,
			Command: "touch " + shellquote.Literal(clearMarker),
		})
		runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
			Matcher: "other", Type: "command", Timeout: 5,
			Command: "touch " + shellquote.Literal(otherMarker),
		})
		sess := newSession(t)
		sess.hookRunner = runner
		drainSessionEvents(sess)
		if reason == "" {
			close(sess)
		} else {
			sess.CloseWithReason(reason)
		}
		_, clearErr := os.Stat(clearMarker)
		_, otherErr := os.Stat(otherMarker)
		return clearErr == nil, otherErr == nil, decodeHookCapture(t, capture)
	}

	clearFired, otherFired, captured := closeWith("clear", func(s *Session) { s.CloseWithReason("clear") })
	if !clearFired || otherFired {
		t.Fatalf("a clear close fired clear=%v other=%v, want only clear", clearFired, otherFired)
	}
	if len(captured) != 1 {
		t.Fatalf("SessionEnd ran %d time(s) for a clear close, want 1", len(captured))
	}
	if got, _ := captured[0]["reason"].(string); got != "clear" {
		t.Errorf("reason = %q, want clear", got)
	}

	plainFiredClear, plainFiredOther, plainCaptured := closeWith("", func(s *Session) { s.Close() })
	if plainFiredClear || !plainFiredOther {
		t.Fatalf("a plain close fired clear=%v other=%v, want only other", plainFiredClear, plainFiredOther)
	}
	if len(plainCaptured) != 1 {
		t.Fatalf("SessionEnd ran %d time(s) for a plain close, want 1", len(plainCaptured))
	}
	if got, _ := plainCaptured[0]["reason"].(string); got != "other" {
		t.Errorf("reason = %q, want other", got)
	}
}

// TestNotification_CarriesTypeAndTitle proves a diagnostic warning fires the
// "warning"-matched hook with notification_type present, and an
// "idle_prompt"-matched hook does not fire.
func TestNotification_CarriesTypeAndTitle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	capture := filepath.Join(dir, "notification-input.jsonl")
	idleMatched := filepath.Join(dir, "idle-prompt-matched")
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "warning", Type: "command", Timeout: 5,
		Command: appendStdinHook(capture),
	})
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "idle_prompt", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(idleMatched),
	})
	sess := newSession(t)
	sess.hookRunner = runner
	drainSessionEvents(sess)

	sess.emit(events.EventWarning, events.WarningData{
		Source:  "evener",
		Title:   "Context warning",
		Message: "the context window is filling up",
	})

	if _, err := os.Stat(idleMatched); !os.IsNotExist(err) {
		t.Fatalf("an idle_prompt-matched Notification hook fired for a warning; stat err = %v", err)
	}
	captured := decodeHookCapture(t, capture)
	if len(captured) != 1 {
		t.Fatalf("warning-matched Notification hook ran %d time(s), want 1", len(captured))
	}
	if got := captured[0]["notification_type"]; got != "warning" {
		t.Errorf("notification_type = %v, want warning", got)
	}
	if got := captured[0]["title"]; got != "Context warning" {
		t.Errorf("title = %v, want the warning's title", got)
	}
}
