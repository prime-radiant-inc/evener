package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// TestAskUser_RejectedInterruptUsesDurableBoundary covers a clean rollback of
// the tool-results record that posted ask_user. The rejected interrupt marker
// must settle from the transcript, so neither the in-memory phantom result nor
// its ask_pending side effect can survive the boundary.
func TestAskUser_RejectedInterruptUsesDurableBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process provider and local transcript I/O; 30s is
	// far above the expected completion time and only guards a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	turnCtx, cancelTurn := context.WithCancel(parentCtx)
	defer cancelTurn()

	ask := askUserCall("ask1", askUserArgsValid())
	cancelCall := llm.ToolCallData{ID: "cancel1", Name: "cancel_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, cancelCall) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	markerFailure := errors.New("interrupt marker rejected after clean rollback")
	toolResultsFailure := errors.New("tool results record rejected")
	var fs *environmentSyncFailureFS
	sess.RegisterTool("cancel_tool", "cancels after ask_user posts",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			fs.mu.Lock()
			fs.writeFailure = toolResultsFailure
			fs.failure = markerFailure
			fs.mu.Unlock()
			cancelTurn()
			return "canceling", nil
		})
	fs = attachEnvironmentFailureFS(t, sess)

	_, processErr := sess.ProcessInput(turnCtx, "which db should we use?", nil)
	if !errors.Is(processErr, context.Canceled) || !errors.Is(processErr, markerFailure) {
		t.Fatalf("ProcessInput error = %v, want cancellation and rejected marker", processErr)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live ask pending count after rejected marker = %d, want 0", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("live state after rejected marker = %q, want %q", got, SessionIdle)
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored ask pending count = %d, want 0", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q", got, SessionIdle)
	}
}
