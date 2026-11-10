package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
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

// TestStop_StopHookActiveResetsWithTheTurn proves stop_hook_active is per TURN,
// not per process: a turn whose Stop hook blocked leaves the flag set, and the
// next turn's first Stop dispatch must still report false. Removing the
// turn-start reset fails this test.
func TestStop_StopHookActiveResetsWithTheTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	capture := filepath.Join(dir, "stop-input.jsonl")
	// The hook records every dispatch and blocks only while the marker exists,
	// so turn one's boundary blocks and turn two's does not.
	marker := filepath.Join(dir, "block-turn-one")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatalf("write block marker: %v", err)
	}
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: appendStdinHook(capture) +
			"; if [ -e " + shellquote.Literal(marker) + ` ]; then printf '%s' '{"decision":"block","reason":"keep going"}'; fi`,
	})
	// The second turn's first model call observes the flag. A tool call would
	// clear it before the Stop dispatch, so the model call is the only point in
	// a turn that can prove the TURN-START reset and nothing else.
	var flagAtSecondTurnStart *bool
	var sess *Session
	sess = newSession(t, withDir(dir), withSteps(
		func(llm.Request) llm.Response {
			sess.mu.Lock()
			active := sess.stopHookActive
			sess.mu.Unlock()
			flagAtSecondTurnStart = &active
			return toolCallResponse(communicateCall("c1", "turn two reply"))
		},
	))
	sess.hookRunner = runner
	drainSessionEvents(sess)

	// Turn one ends with a Stop block, so its flag is set when the turn returns.
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.comm = communicateResult{called: true, reply: "turn one reply"}
	sess.mu.Unlock()
	if done, _, err := sess.deliverIfCommunicated(context.Background(), false); err != nil || done {
		t.Fatalf("turn one boundary = done %v, err %v; want a blocked (not done) boundary", done, err)
	}
	sess.mu.Lock()
	wasActive := sess.stopHookActive
	sess.mu.Unlock()
	if !wasActive {
		t.Fatal("setup: the Stop block did not set stopHookActive")
	}

	// Turn two starts through the real turn-start path, which is what must
	// clear it. It runs one scripted turn, then dispatches Stop again.
	before := len(decodeHookCapture(t, capture))
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove block marker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter and one local shell hook; only fires on a genuine hang.
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "second turn", nil); err != nil {
		t.Fatalf("turn two: %v", err)
	} else if strings.TrimSpace(out) != "turn two reply" {
		t.Fatalf("turn two delivered %q, want its communicate reply", out)
	}

	if flagAtSecondTurnStart == nil {
		t.Fatal("turn two never reached its model call")
	}
	if *flagAtSecondTurnStart {
		t.Fatal("the second turn's first model call saw stop_hook_active still set; the per-turn reset did not run")
	}

	captured := decodeHookCapture(t, capture)
	if len(captured) <= before {
		t.Fatalf("turn two dispatched no Stop hook: %d line(s), %d before", len(captured), before)
	}
	active, present := captured[before]["stop_hook_active"].(bool)
	if !present || active {
		t.Fatalf("the next turn's first dispatch stop_hook_active = %v (present=%v), want false", captured[before]["stop_hook_active"], present)
	}
	if msg, _ := captured[before]["last_assistant_message"].(string); msg != "turn two reply" {
		t.Errorf("last_assistant_message = %q, want the second turn's reply", msg)
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
	closeWith := func(reason string, closeFn func(*Session)) (clearFired, otherFired bool, captured []map[string]any) {
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
			closeFn(sess)
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

// sessionEndReasonProbe closes one session through the daemon's shutdown path
// (CloseForShutdown) and reports which reason hooks fired and what reason the
// hook input carried.
func sessionEndReasonProbe(t *testing.T, seed func(*Session)) (promptExit, other bool, captured []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	stdin := filepath.Join(dir, "session-end-input.jsonl")
	promptMarker := filepath.Join(dir, "prompt-input-exit-matched")
	otherMarker := filepath.Join(dir, "other-matched")
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: appendStdinHook(stdin),
	})
	runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "prompt_input_exit", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(promptMarker),
	})
	runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "other", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(otherMarker),
	})
	sess := newSession(t)
	sess.hookRunner = runner
	drainSessionEvents(sess)
	if seed != nil {
		seed(sess)
	}
	sess.CloseForShutdown()
	_, promptErr := os.Stat(promptMarker)
	_, otherErr := os.Stat(otherMarker)
	return promptErr == nil, otherErr == nil, decodeHookCapture(t, stdin)
}

// TestSessionEnd_PromptInputExitNeedsAnIdleShutdown proves Claude's
// prompt_input_exit is only claimed where evener can prove it: the daemon's
// shutdown close of an idle session. An idle shutdown fires the
// prompt_input_exit hook and not the other hook. So does a shutdown with a
// question on screen: a pending ask IS prompt input, so it is the most literal
// case rather than in-flight work. A shutdown with real work in flight (a
// running job, or a turn killed mid-stream) fires the other hook and not the
// prompt_input_exit one, because the user did not exit a prompt — the session
// died under work.
func TestSessionEnd_PromptInputExitNeedsAnIdleShutdown(t *testing.T) {
	t.Parallel()

	t.Run("idle", func(t *testing.T) {
		promptExit, other, captured := sessionEndReasonProbe(t, nil)
		if !promptExit || other {
			t.Fatalf("an idle shutdown fired prompt_input_exit=%v other=%v, want only prompt_input_exit", promptExit, other)
		}
		if len(captured) != 1 {
			t.Fatalf("SessionEnd ran %d time(s), want 1", len(captured))
		}
		if got, _ := captured[0]["reason"].(string); got != "prompt_input_exit" {
			t.Errorf("reason = %q, want prompt_input_exit", got)
		}
	})

	t.Run("pending ask", func(t *testing.T) {
		promptExit, other, captured := sessionEndReasonProbe(t, func(s *Session) {
			s.mu.Lock()
			s.setAskPendingLocked([]askQuestion{{Question: "which database?"}})
			s.mu.Unlock()
		})
		if !promptExit || other {
			t.Fatalf("a shutdown with a question on screen fired prompt_input_exit=%v other=%v, want only prompt_input_exit", promptExit, other)
		}
		if len(captured) != 1 {
			t.Fatalf("SessionEnd ran %d time(s), want 1", len(captured))
		}
		if got, _ := captured[0]["reason"].(string); got != "prompt_input_exit" {
			t.Errorf("reason = %q, want prompt_input_exit", got)
		}
	})

	t.Run("running job", func(t *testing.T) {
		promptExit, other, captured := sessionEndReasonProbe(t, func(s *Session) {
			// A zero grace keeps the close's own wait from spending its window;
			// the assertion is about the reason sampled before teardown.
			s.jobManager.closeGrace = 0
			if _, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"}); err != nil {
				t.Fatalf("create running job: %v", err)
			}
		})
		if promptExit || !other {
			t.Fatalf("a shutdown with a running job fired prompt_input_exit=%v other=%v, want only other", promptExit, other)
		}
		if len(captured) != 1 {
			t.Fatalf("SessionEnd ran %d time(s), want 1", len(captured))
		}
		if got, _ := captured[0]["reason"].(string); got != "other" {
			t.Errorf("reason = %q, want other", got)
		}
	})

	// A turn killed mid-stream settles itself to SessionIdle and clears its
	// pending ask (the interrupt branch), so a shutdown after it looks idle
	// unless the interrupt is recorded. Without that marker this subtest fires
	// prompt_input_exit for a user who killed a working session.
	t.Run("interrupted turn", func(t *testing.T) {
		promptExit, other, captured := sessionEndReasonProbe(t, func(s *Session) {
			// A blocking job stands in for the shutdown: the turn is cancelled
			// the way a SIGTERM cancels it, and ProcessInput settles idle.
			trigger := llm.ToolCallData{ID: "interrupt1", Name: "trigger_interrupt", Arguments: json.RawMessage(`{}`), Type: "function"}
			f := &fakeAdapter{
				name: "openai",
				steps: []func(llm.Request) llm.Response{
					func(llm.Request) llm.Response { return toolCallResponse(trigger) },
				},
			}
			s.client.Register(f)

			ctx, cancel := context.WithCancel(context.Background())
			s.RegisterTool("trigger_interrupt", "cancels the test's ProcessInput context mid-turn",
				map[string]any{"type": "object", "properties": map[string]any{}},
				func(context.Context, any) (any, error) {
					cancel()
					return "cancelling", nil
				})
			if _, err := s.ProcessInput(ctx, "do a long turn", nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("ProcessInput err = %v, want context.Canceled", err)
			}
			if got := s.State(); got != SessionIdle {
				t.Fatalf("state after interrupt = %q, want %q (the settle that hides the interrupt)", got, SessionIdle)
			}
		})
		if promptExit || !other {
			t.Fatalf("a shutdown after an interrupted turn fired prompt_input_exit=%v other=%v, want only other", promptExit, other)
		}
		if len(captured) != 1 {
			t.Fatalf("SessionEnd ran %d time(s), want 1", len(captured))
		}
		if got, _ := captured[0]["reason"].(string); got != "other" {
			t.Errorf("reason = %q, want other", got)
		}
	})
}
