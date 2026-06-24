package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/llm"
)

// testGoalSession builds a minimal Session with a registered fakeAdapter, suitable
// for invoking tools through sess.reg.ExecuteCall.
func testGoalSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// testGoalNow returns a fixed time for deterministic tests.
func testGoalNow() time.Time { return time.Unix(0, 0).UTC() }

func TestUpdateGoalTool_Complete(t *testing.T) {
	t.Parallel()
	sess := testGoalSession(t)
	sess.getOrCreateGoalStore().Set("write a test", testGoalNow())

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "g1",
		Name:      "update_goal",
		Arguments: json.RawMessage(`{"status":"complete"}`),
	})
	if res.IsError {
		t.Fatalf("update_goal complete: unexpected error: %s", res.Output)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("expected goal to still exist after update")
	}
	if snap.Status != goal.StatusComplete {
		t.Fatalf("status = %v, want complete", snap.Status)
	}
}

func TestUpdateGoalTool_Blocked(t *testing.T) {
	t.Parallel()
	sess := testGoalSession(t)
	sess.getOrCreateGoalStore().Set("do something impossible", testGoalNow())

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "g2",
		Name:      "update_goal",
		Arguments: json.RawMessage(`{"status":"blocked"}`),
	})
	if res.IsError {
		t.Fatalf("update_goal blocked: unexpected error: %s", res.Output)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("expected goal to still exist after update")
	}
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %v, want blocked", snap.Status)
	}
}

func TestUpdateGoalTool_RejectsBadStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   string
		args string
		kind string
	}{
		{"invalid status", "g3", `{"status":"frobnicate"}`, "invalid"},
		{"missing status", "g4", `{}`, "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := testGoalSession(t)
			sess.getOrCreateGoalStore().Set("obj", testGoalNow())

			res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID:        tc.id,
				Name:      "update_goal",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("%s status should produce IsError=true, got output: %s", tc.kind, res.Output)
			}
		})
	}
}

func TestUpdateGoalTool_NoActiveGoal(t *testing.T) {
	t.Parallel()
	sess := testGoalSession(t)
	// Explicitly ensure no goal is set (store is empty on creation, but be explicit).
	sess.getOrCreateGoalStore().Clear()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "g5",
		Name:      "update_goal",
		Arguments: json.RawMessage(`{"status":"complete"}`),
	})
	if res.IsError {
		t.Fatalf("no active goal should not be IsError, got: %s", res.Output)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty output for no active goal case")
	}
	// The goal store should remain empty (no goal to transition).
	_, ok := sess.getOrCreateGoalStore().Snapshot()
	if ok {
		t.Fatal("no goal should be present after update on empty store")
	}
}
