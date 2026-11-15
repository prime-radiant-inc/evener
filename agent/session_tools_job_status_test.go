package agent

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

func TestJobStatusConsistentKeysShell(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30", Description: "test shell"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	status, err := jobStatusTool(s, map[string]any{"target": rec.JobID}, 1<<20)
	if err != nil {
		t.Fatalf("job_status: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(handlerJSON(t, status), &state); err != nil {
		t.Fatalf("decode job_status: %v", err)
	}

	if state["id"] != rec.JobID {
		t.Errorf("shell job_status id = %v, want %q", state["id"], rec.JobID)
	}
	if state["job_id"] != rec.JobID {
		t.Errorf("shell job_status job_id = %v, want %q", state["job_id"], rec.JobID)
	}
	if state["type"] != "shell" {
		t.Errorf("shell job_status type = %v, want shell", state["type"])
	}
	if state["kind"] != "shell" {
		t.Errorf("shell job_status kind = %v, want shell", state["kind"])
	}
}

func TestJobStatusConsistentKeysDelegate(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	id := "dlg_status_keys"
	started := time.Unix(100, 0).UTC()
	seedStableToolDelegate(t, s, id, "", started, started.Add(time.Second))

	status, err := jobStatusTool(s, map[string]any{"target": id}, 1<<20)
	if err != nil {
		t.Fatalf("job_status: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(handlerJSON(t, status), &state); err != nil {
		t.Fatalf("decode job_status: %v", err)
	}

	if state["id"] != id {
		t.Errorf("delegate job_status id = %v, want %q", state["id"], id)
	}
	if jobID, exists := state["job_id"]; exists && jobID != nil {
		t.Errorf("delegate job_status must not expose job_id, got %v", jobID)
	}
	if state["type"] != "delegate" {
		t.Errorf("delegate job_status type = %v, want delegate", state["type"])
	}
	if kind, exists := state["kind"]; exists && kind != nil {
		t.Errorf("delegate job_status must not expose kind, got %v", kind)
	}
	if state["phase"] != "idle" {
		t.Errorf("delegate job_status phase = %v, want idle", state["phase"])
	}
}

func TestJobStatusDelegatePromptDeduplication(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.stateDir = t.TempDir()
	promptText := "Detailed multi-paragraph prompt for delegate research and execution"
	res := s.reg.ExecuteCall(t.Context(), s.env, llm.ToolCallData{
		ID:        "call_del",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"prompt":` + mustJSONString(promptText) + `}`),
	})
	if res.IsError {
		t.Fatalf("delegate create error: %s", res.Output)
	}
	var receipt delegateCreateResult
	if err := json.Unmarshal([]byte(res.Output), &receipt); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	id := receipt.DelegateID

	status, err := jobStatusTool(s, map[string]any{"target": id}, 1<<20)
	if err != nil {
		t.Fatalf("job_status: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(handlerJSON(t, status), &state); err != nil {
		t.Fatalf("decode job_status: %v", err)
	}

	if state["task"] != promptText {
		t.Errorf("task = %v, want %q", state["task"], promptText)
	}
	if desc, exists := state["description"]; exists && desc != "" {
		t.Errorf("description should be omitted when identical to task, got %v", desc)
	}

	// Now verify that when description is distinct from task, it IS kept.
	id2 := "dlg_prompt_distinct"
	descriptor2 := stableToolDescriptor(s, id2, "")
	descriptor2.Task = promptText
	descriptor2.Description = "Short distinct summary"

	c := s.delegateController
	c.mu.Lock()
	started := time.Unix(100, 0).UTC()
	_, err = c.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id2, Created: &delegatestore.DelegateCreated{Descriptor: descriptor2}},
		delegateControllerRunStartedEvent(id2, 1, delegatestore.TriggerInitial, started),
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	status2, err := jobStatusTool(s, map[string]any{"target": id2}, 1<<20)
	if err != nil {
		t.Fatalf("job_status distinct: %v", err)
	}
	var state2 map[string]any
	if err := json.Unmarshal(handlerJSON(t, status2), &state2); err != nil {
		t.Fatalf("decode job_status distinct: %v", err)
	}
	if state2["description"] != "Short distinct summary" {
		t.Errorf("distinct description = %v, want 'Short distinct summary'", state2["description"])
	}
}
