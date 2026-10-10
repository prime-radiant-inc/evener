package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// An interrupted turn's SESSION_END states the session's wire state, the same
// one a resting session reports, so a server that takes its state from the
// session's events alone shows work still waiting to run (a job notification
// that arrived during the turn) as active rather than idle.
func TestInterruptedSessionEndCarriesTheWireState(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	started := make(chan struct{}, 1)
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
			}}}
		},
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow", Description: "Wait for cancellation."},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, _ map[string]any) (any, error) {
			sess.enqueueJobNotification(jobNotification{JobID: "job_during_turn"})
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	evs, mu, done := collectEvents(sess)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	outer, cancelOuter := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOuter()
	turnCtx, cancelTurn := context.WithCancel(outer)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = sess.ProcessInput(turnCtx, "run", nil)
	}()
	select {
	case <-started:
	case <-outer.Done():
		t.Fatal("the tool never started")
	}
	cancelTurn()
	<-finished
	want := sess.WireState()
	sess.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *evs {
		if d, ok := ev.Data.(events.SessionEndData); ok && d.Interrupted {
			if d.State != want || want != string(SessionProcessing) {
				t.Fatalf("interrupted SESSION_END state = %q, wire state %q; want both active with a notification waiting", d.State, want)
			}
			return
		}
	}
	t.Fatal("no interrupted SESSION_END")
}
