package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// writeFileCall builds a write_file tool call. write_file is !ReadOnly and is
// neither the result tool nor task_list, so it is a "progress" call for the goal
// no-progress signal (spec §2). It writes to path under the session's working
// directory through the real execution environment.
func writeFileCall(id, path, content string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"file_path": path, "content": content})
	return llm.ToolCallData{ID: id, Name: "write_file", Arguments: args, Type: "function"}
}

// updateGoalCall builds an update_goal tool call with the given status.
func updateGoalCall(id, status string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"status": status})
	return llm.ToolCallData{ID: id, Name: "update_goal", Arguments: args, Type: "function"}
}

func countGoalContinuations(evs []events.SessionEvent) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == events.EventGoalContinuation {
			n++
		}
	}
	return n
}

// TestGoalLoopRunsContinuationToComplete is THE deterministic proof: it drives
// the real ProcessInputKind drain loop and the continuation gate through multiple
// turns to a clean completion, with no API key and no live model.
//
// The fake model is scripted so that:
//   - turn 1 (the user's "begin") makes a mutating write_file call, then ends the
//     turn via the result tool WITHOUT calling update_goal — the gate must inject
//     a continuation because the goal is still active and the turn progressed;
//   - turn 2 (the injected continuation) calls update_goal{status:complete}, then
//     ends the turn — the gate sees the terminal status and stops the loop.
//
// The non-negotiable assertions are count(EventGoalContinuation) >= 1 (the loop
// actually ran a continuation) and exactly one EventGoalEnded{complete}. It also
// asserts the continuation turn entered history as schema.TurnSteering, not a
// user turn.
func TestGoalLoopRunsContinuationToComplete(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn 1, round 0: mutate (progress) but do NOT declare complete.
			func(req llm.Request) llm.Response {
				return toolCallResponse(writeFileCall("w1", "progress.txt", "work"))
			},
			// Turn 1, round 1: end the turn via the result tool (idle). The goal
			// is still active, so the gate injects a continuation.
			func(req llm.Request) llm.Response { return finalResponse("did some work") },
			// Turn 2 (continuation), round 0: declare the goal complete.
			func(req llm.Request) llm.Response {
				return toolCallResponse(updateGoalCall("g1", "complete"))
			},
			// Turn 2, round 1: end the turn. The gate sees the terminal status and stops.
			func(req llm.Request) llm.Response { return finalResponse("goal achieved") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	sess.getOrCreateGoalStore().Set("demo objective", time.Now())

	stop := drainEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "begin", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The continuation turn(s) must have entered history as steering, not as user
	// input. Capture this before Close so the assertion reflects the loop's turns.
	sess.mu.Lock()
	var sawSteeringContinuation bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering {
			sawSteeringContinuation = true
			break
		}
	}
	historyKinds := turnKinds(sess.history)
	sess.mu.Unlock()

	sess.Close()
	evs := stop()

	gotContinuations := countGoalContinuations(evs)
	if gotContinuations < 1 {
		t.Fatalf("count(EventGoalContinuation) = %d, want >= 1 (the gate must have run a continuation); history kinds=%s", gotContinuations, historyKinds)
	}
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("count(EventGoalEnded) = %d, want exactly 1", got)
	}
	ended := lastGoalEnded(t, evs)
	if ended.Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want %q", ended.Status, "complete")
	}
	t.Logf("PROOF: count(EventGoalContinuation)=%d (>=1); EventGoalEnded{Status:%q Iterations:%d}; history kinds=%s",
		gotContinuations, ended.Status, ended.Iterations, historyKinds)
	if !sawSteeringContinuation {
		t.Fatalf("continuation turn should be schema.TurnSteering, not a user turn; history kinds=%s", historyKinds)
	}
}
