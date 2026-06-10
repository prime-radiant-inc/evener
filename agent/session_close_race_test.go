package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestSession_Close_NoRaceWithSubagentEmit reproduces PRI-1939: a subagent's run
// goroutine emits a job event through the parent's emit() at the same moment
// Close() closes the events channel. Run under the race detector; before the fix
// this trips a DATA RACE between the chansend in (*Session).emit and the
// close(s.events) in (*Session).Close. After the fix Close() joins the subagent
// goroutine before closing, so the emit always precedes the close.
func TestSession_Close_NoRaceWithSubagentEmit(t *testing.T) {
	for i := 0; i < 30; i++ {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return finalResponse("done") },
			},
		})
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
			ID:        "c1",
			Name:      "spawn_agent",
			Arguments: json.RawMessage(`{"task":"do it"}`),
		})
		if spawnRes.IsError {
			sess.Close()
			t.Fatalf("spawn_agent error: %s", spawnRes.Output)
		}
		// Close immediately, racing the subagent's detached event emission.
		sess.Close()
	}
}

// TestSession_Close_NoRaceWithConcurrentEmit covers the PRI-1939 residual: a
// caller-owned goroutine emitting (e.g. Enqueue's EventQueueChanged, or the
// ProcessInput loop) concurrently with Close() closing the events channel. The
// session cannot join caller-owned goroutines, so before the fix emit() relied
// on a defer-recover() to swallow the resulting send-on-closed-channel — a real
// DATA RACE the detector flags. After the fix, emit and close are mutually
// excluded by eventsMu, so the send never races the close and recover() is gone.
// This hammers emit() directly (the mechanism under test) for a reliable repro.
func TestSession_Close_NoRaceWithConcurrentEmit(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return finalResponse("done") },
			},
		})
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				sess.emit(events.EventWarning, events.WarningData{Message: "concurrent"})
			}
		}()
		// Close concurrently with the caller-owned emits.
		sess.Close()
		wg.Wait()
	}
}
