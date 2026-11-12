package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/llm"
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
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
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
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
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
	wantOutput := "No goal is active for this session (none was set at launch); nothing recorded — this tool only updates a goal the harness registered."
	if res.Output != wantOutput {
		t.Fatalf("Output = %q, want %q", res.Output, wantOutput)
	}
	// The goal store should remain empty (no goal to transition).
	_, ok := sess.getOrCreateGoalStore().Snapshot()
	if ok {
		t.Fatal("no goal should be present after update on empty store")
	}
}

func TestUpdateGoalTool_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		initialStatus goal.Status
		updateStatus  string
		wantStatus    string
	}{
		{
			name:          "already complete",
			initialStatus: goal.StatusComplete,
			updateStatus:  "blocked",
			wantStatus:    "complete",
		},
		{
			name:          "already blocked",
			initialStatus: goal.StatusBlocked,
			updateStatus:  "complete",
			wantStatus:    "blocked",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := testGoalSession(t)
			sess.getOrCreateGoalStore().Set("seeded objective", testGoalNow())
			if !sess.getOrCreateGoalStore().SetTerminal(tc.initialStatus, "seed-reason", testGoalNow()) {
				t.Fatalf("setup: failed to set goal %v", tc.initialStatus)
			}

			noGoalSess := testGoalSession(t)
			noGoalRes := noGoalSess.reg.ExecuteCall(context.Background(), noGoalSess.env, llm.ToolCallData{
				ID:        "g-none",
				Name:      "update_goal",
				Arguments: json.RawMessage(`{"status":"complete"}`),
			})

			res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID:        "g-term",
				Name:      "update_goal",
				Arguments: json.RawMessage(`{"status":"` + tc.updateStatus + `"}`),
			})
			if res.IsError {
				t.Fatalf("update_goal on already terminal goal should not be IsError, got: %s", res.Output)
			}
			snap, ok := sess.getOrCreateGoalStore().Snapshot()
			if !ok || snap.Status != tc.initialStatus || snap.StopReason != "seed-reason" {
				t.Fatalf("goal store changed: %+v", snap)
			}
			assertNoGoalUpdated(t, sess)
			if len(res.ToolState) == 0 {
				t.Fatal("expected non-empty ToolState for already-terminal goal")
			}
			var state map[string]any
			if err := json.Unmarshal(res.ToolState, &state); err != nil {
				t.Fatalf("unmarshal ToolState: %v", err)
			}
			if state["status"] != tc.wantStatus {
				t.Fatalf("ToolState status = %v, want %q", state["status"], tc.wantStatus)
			}
			if state["stopReason"] != "seed-reason" {
				t.Fatalf("ToolState stopReason = %v, want seed-reason", state["stopReason"])
			}
			if res.Output == "" || res.Output == noGoalRes.Output {
				t.Fatalf("expected distinct output from no-goal fallback, got %q", res.Output)
			}
		})
	}
}

func TestUpdateGoal_VisibilityFollowsGoalConfiguration(t *testing.T) {
	t.Parallel()
	sess := testGoalSession(t)

	// Goal-less session at launch: update_goal must NOT be advertised in cachedToolDefs or ToolDefinitions.
	if hasToolDef(sess.cachedToolDefs, "update_goal") {
		t.Fatal("update_goal should not be advertised in cachedToolDefs when no goal is configured")
	}
	if hasToolDef(sess.ToolDefinitions(), "update_goal") {
		t.Fatal("update_goal should not be advertised in ToolDefinitions() when no goal is configured")
	}

	// Dynamically attaching a goal via SetGoal: update_goal becomes visible.
	ctx := context.Background()
	if _, err := sess.SetGoal(ctx, "dynamic objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if !hasToolDef(sess.cachedToolDefs, "update_goal") {
		t.Fatal("update_goal should be advertised in cachedToolDefs after SetGoal")
	}
	if !hasToolDef(sess.ToolDefinitions(), "update_goal") {
		t.Fatal("update_goal should be advertised in ToolDefinitions() after SetGoal")
	}

	// Clearing the goal via ClearGoal: update_goal is hidden again.
	if err := sess.ClearGoal(); err != nil {
		t.Fatalf("ClearGoal: %v", err)
	}
	if hasToolDef(sess.cachedToolDefs, "update_goal") {
		t.Fatal("update_goal should not be advertised in cachedToolDefs after ClearGoal")
	}
	if hasToolDef(sess.ToolDefinitions(), "update_goal") {
		t.Fatal("update_goal should not be advertised in ToolDefinitions() after ClearGoal")
	}
}

func TestHasGoalConfigured_RaceWithGoalStoreInit(t *testing.T) {
	t.Parallel()
	for range 100 {
		sess := &Session{}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = sess.hasGoalConfigured()
		}()
		go func() {
			defer wg.Done()
			_, _, _ = sess.GoalStatus()
		}()
		wg.Wait()
	}
}
