package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// TestTaskTool_AppendEmitsTaskUpdated and TestTaskTool_UpdateToDoneEmitsTaskUpdated
// drive the task_list tool through registerTaskTools with a fake toolDeps (no
// real *Session), the same seam TestToolDeps_ShellTimeoutClamp and
// TestToolDeps_ReadBeforeWriteWarning use in session_tooldeps_test.go. They
// prove Correctness T9's emit sites: an append that changes Total, and a
// completing update that changes Done, each publish an EventTaskUpdated with
// the store's post-mutation Progress().

func TestTaskTool_AppendEmitsTaskUpdated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "emit-test")
	store.Load()

	var emitted []events.EventData
	deps := &toolDeps{
		emit:  func(kind events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer: func(string) {},
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
			setReasoningEffort:   func(string) {},
		},
	}
	reg := tool.NewRegistry()
	registerTaskTools(reg, deps)

	args, _ := json.Marshal(map[string]any{
		"action": "append",
		"tasks": []map[string]any{
			{"type": "implement", "description": "a", "prompt": "p"},
		},
	})
	res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: "c1", Name: "task_list", Arguments: args})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}

	var found *events.TaskUpdatedData
	for _, d := range emitted {
		if td, ok := d.(events.TaskUpdatedData); ok {
			found = &td
		}
	}
	if found == nil {
		t.Fatalf("append did not emit EventTaskUpdated; emitted=%+v", emitted)
	}
	if found.Total != 1 || found.Done != 0 {
		t.Fatalf("TaskUpdatedData = %+v, want Total=1 Done=0", *found)
	}
}

func TestTaskTool_UpdateToDoneEmitsTaskUpdated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "emit-test-2")
	store.Load()
	store.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeImplement, Description: "a", Prompt: "p"}})

	var emitted []events.EventData
	deps := &toolDeps{
		emit:  func(kind events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer: func(string) {},
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
			setReasoningEffort:   func(string) {},
		},
	}
	reg := tool.NewRegistry()
	registerTaskTools(reg, deps)

	args, _ := json.Marshal(map[string]any{
		"action":  "update",
		"updates": []map[string]any{{"id": 1, "status": "done"}},
	})
	res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: "c1", Name: "task_list", Arguments: args})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}

	var found *events.TaskUpdatedData
	for _, d := range emitted {
		if td, ok := d.(events.TaskUpdatedData); ok {
			found = &td
		}
	}
	if found == nil {
		t.Fatalf("a completing update did not emit EventTaskUpdated; emitted=%+v", emitted)
	}
	if found.Total != 1 || found.Done != 1 {
		t.Fatalf("TaskUpdatedData = %+v, want Total=1 Done=1", *found)
	}
}
