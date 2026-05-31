package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// TestSession_Close_NoRaceWithSubagentEmit reproduces PRI-1939: a subagent's run
// goroutine emits EventSubagentEnd through the parent's emit() at the same moment
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
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
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
		// Close immediately, racing the subagent's EventSubagentEnd emit.
		sess.Close()
	}
}
