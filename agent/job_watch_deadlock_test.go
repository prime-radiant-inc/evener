package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

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
// tool round and then communicates "done", and installs a
// caller-send watch on the given event kind BEFORE the turn is driven.
func newCallerSendWatchSession(t *testing.T, watchEvent string) *Session {
	t.Helper()
	// StateDir mirrors the persisted-session incident shape: in the pre-mailbox
	// design a caller watch-send on a transcript-backed session re-locked
	// responseSideEffectsMu under the emit and wedged the loop. Observation is now
	// persist-only (it enqueues a wake token, never delivers), so this drives the
	// same firing path and proves the wedge is gone.
	sess := newSession(t,
		withSteps(
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellExecCall("s1")) },
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
		),
		withConfig(SessionConfig{StateDir: t.TempDir()}),
	)

	// Install the caller-send watch BELOW the validation layer. configureWatch
	// (and the job_watch tool) now reject this exact shape as a feedback loop
	// (target=caller, send.to=caller on a self-generated kind). The loop-prevention
	// guard is asserted separately by TestValidateWatchDeliveryLoop; here we need a
	// live caller-send watch on jm.watches so driveWithWatchdog exercises the real
	// firing+delivery path (onSessionEvent -> recordWatchSendsAndKick -> token ->
	// drain). newWatchConfig runs no loop guard, so this direct install is legal
	// and mirrors exactly what configureWatch builds and installs.
	installWatchBelowValidation(t, sess.jobManager, watchArgs{
		Target: "caller",
		Events: []string{watchEvent},
		Send:   &watchSendArgs{To: "caller", Message: "ping"},
	})
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

func TestCallerSendWatchDoesNotDeadlockOnCommunicateEvents(t *testing.T) {
	t.Parallel()
	sess := newCallerSendWatchSession(t, "communicate")
	driveWithWatchdog(t, sess)
	assertToolRoundPersisted(t, sess)
}

func TestCallerSendWatchDoesNotDeadlockOnToolEvents(t *testing.T) {
	t.Parallel()
	sess := newCallerSendWatchSession(t, "assistant.tool")
	driveWithWatchdog(t, sess)
	assertToolRoundPersisted(t, sess)
}
