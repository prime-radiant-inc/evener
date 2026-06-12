package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// shellExecCall builds an exec_command (OpenAI-mapped "shell") tool call.
func shellExecCall(id string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{
		"command":     "echo ok",
		"description": "filler",
	})
	return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
}

// newCallerSendWatchSession builds a session whose scripted provider runs one
// tool round (so EventToolCallEnd AND EventAssistantTextEnd both fire under
// responseSideEffectsMu) and then communicates "done", and installs a
// caller-send watch on the given event kind BEFORE the turn is driven.
func newCallerSendWatchSession(t *testing.T, watchEvent string) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellExecCall("s1")) },
		func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	}})
	// StateDir mirrors the persisted-session incident shape: in the pre-mailbox
	// design a caller watch-send on a transcript-backed session re-locked
	// responseSideEffectsMu under the emit and wedged the loop. Observation is now
	// persist-only (it enqueues a wake token, never delivers), so this drives the
	// same firing path and proves the wedge is gone.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	watchArgsJSON, err := json.Marshal(map[string]any{
		"target": "caller",
		"events": []string{watchEvent},
		"send":   map[string]any{"to": "caller", "message": "ping"},
	})
	if err != nil {
		t.Fatalf("marshal watch args: %v", err)
	}
	watchRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(watchArgsJSON),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch: %s", watchRes.Output)
	}
	return sess
}

// assertToolRoundPersisted fails unless the session history carries both the
// assistant turn that issued the tool call and the TOOL_RESULTS turn that
// recorded its result. The deadlock incident lost the TOOL_RESULTS, so the
// tool-result turn is the load-bearing assertion.
func assertToolRoundPersisted(t *testing.T, sess *Session) {
	t.Helper()
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()

	var sawAssistantToolCall, sawToolResults bool
	for _, turn := range history {
		switch turn.Kind {
		case schema.TurnAssistant:
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.Name == "exec_command" {
					sawAssistantToolCall = true
				}
			}
		case schema.TurnToolResults:
			sawToolResults = true
		}
	}
	if !sawAssistantToolCall {
		t.Fatalf("history missing assistant exec_command tool call; kinds=%s", turnKinds(history))
	}
	if !sawToolResults {
		t.Fatalf("history missing TOOL_RESULTS turn (incident lost tool results); kinds=%s", turnKinds(history))
	}
}

// driveWithWatchdog runs ProcessInput in a goroutine and fails with a full
// stack dump if the turn does not complete within 30s. A watch-send deadlock
// (the pre-mailbox bug, where observation re-locked responseSideEffectsMu under
// the emit) would wedge the loop goroutine, which the stack dump makes visible.
func driveWithWatchdog(t *testing.T, sess *Session) {
	t.Helper()
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sess.ProcessInput(ctx, "run the tool", nil)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		buf := make([]byte, 1<<20)
		t.Fatalf("session wedged (watch-send deadlock):\n%s", buf[:runtime.Stack(buf, true)])
	}
}

func TestCallerSendWatchDoesNotDeadlockOnAssistantEvents(t *testing.T) {
	sess := newCallerSendWatchSession(t, "assistant.message")
	driveWithWatchdog(t, sess)
	assertToolRoundPersisted(t, sess)
}

func TestCallerSendWatchDoesNotDeadlockOnToolEvents(t *testing.T) {
	sess := newCallerSendWatchSession(t, "assistant.tool")
	driveWithWatchdog(t, sess)
	assertToolRoundPersisted(t, sess)
}
