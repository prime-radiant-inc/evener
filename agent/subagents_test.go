package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// newTestSession creates a minimal *Session backed by a no-op fakeAdapter.
// The caller must defer sess.Close().
func newTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("newTestSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// TestResultSnapshot_CurrentShape verifies baseline snapshot fields for a completed subagent.
func TestResultSnapshot_CurrentShape(t *testing.T) {
	a := &subagent{id: "01CHILD", status: SubagentCompleted, result: "done", turnsUsed: 3, sess: newTestSession(t)}
	snap := a.resultSnapshotLocked()
	if snap.Status != SubagentCompleted || snap.Output != "done" || !snap.Success || snap.TurnsUsed != 3 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.TranscriptRef == "" {
		t.Fatal("transcript_ref must be set")
	}
}

// TestResultSnapshot_CarriesAgentIDAndReason verifies that agent_id and reason are stamped on the snapshot.
func TestResultSnapshot_CarriesAgentIDAndReason(t *testing.T) {
	cases := []struct {
		name        string
		status      SubagentStatus
		err         error
		wantReason  SubagentStatus
		wantSuccess bool
	}{
		{"completed", SubagentCompleted, nil, SubagentCompleted, true},
		{"failed", SubagentFailed, errors.New("boom"), SubagentFailed, false},
		{"cancelled", SubagentCancelled, context.Canceled, SubagentCancelled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &subagent{id: "01CHILD", status: tc.status, err: tc.err, sess: newTestSession(t)}
			snap := a.resultSnapshotLocked()
			if snap.AgentID != "01CHILD" || snap.Reason != tc.wantReason || snap.Success != tc.wantSuccess {
				t.Fatalf("got %+v", snap)
			}
		})
	}
}

// TestBlockingSpawn_SnapshotHasAgentID verifies that a blocking spawn result carries agent_id directly from the snapshot.
func TestBlockingSpawn_SnapshotHasAgentID(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("subagent done")
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking spawn returned error: %s", res.Output)
	}

	var result subagentResult
	if err := json.Unmarshal([]byte(res.Output), &result); err != nil {
		t.Fatalf("parsing blocking result: %v (output: %s)", err, res.Output)
	}
	if result.AgentID == "" {
		t.Errorf("agent_id must be set in blocking result, got: %s", res.Output)
	}
	if result.Status != SubagentCompleted {
		t.Errorf("status = %q, want %q", result.Status, SubagentCompleted)
	}
	if result.Reason != SubagentCompleted {
		t.Errorf("reason = %q, want %q", result.Reason, SubagentCompleted)
	}
	if !result.Success {
		t.Error("success must be true for a completed agent")
	}
	if result.TurnsUsed < 1 {
		t.Error("turns_used must be >= 1")
	}
	if result.TranscriptRef == "" {
		t.Error("transcript_ref must be set")
	}
}

// TestSubagentEndData_HasReason verifies that SUBAGENT_END carries a reason field.
func TestSubagentEndData_HasReason(t *testing.T) {
	d := events.SubagentEndData{AgentID: "x", Status: "completed", TurnsUsed: 2, Reason: "completed"}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"reason":"completed"`) {
		t.Fatalf("missing reason: %s", b)
	}
}
