package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
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
		emit:           func(kind events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
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
		emit:           func(kind events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
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

func TestTaskTool_UpdateToInProgressEmitsTaskUpdated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "emit-test-3")
	store.Load()
	store.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeImplement, Description: "first task", Prompt: "p"},
		{Type: taskpkg.TaskTypeImplement, Description: "live current task", Prompt: "p"},
	})

	var emitted []events.EventData
	deps := &toolDeps{
		emit:           func(kind events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
		},
	}
	reg := tool.NewRegistry()
	registerTaskTools(reg, deps)

	args, _ := json.Marshal(map[string]any{
		"action":  "update",
		"updates": []map[string]any{{"id": 2, "status": "in_progress"}},
	})
	res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: "c1", Name: "task_list", Arguments: args})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}

	for _, event := range emitted {
		data, ok := event.(events.TaskUpdatedData)
		if !ok || data.Current == nil || data.Current.ID != 2 || data.Current.Description != "live current task" {
			continue
		}
		return
	}
	t.Fatalf("TASK_UPDATED = %#v", emitted)
}

func TestTaskUpdatedDataUsesFirstInProgressTask(t *testing.T) {
	data := taskUpdatedData(taskpkg.Summarize([]taskpkg.Task{
		{ID: 1, Status: taskpkg.TaskDone},
		{ID: 2, Status: taskpkg.TaskInProgress, Description: "first current task"},
		{ID: 3, Status: taskpkg.TaskInProgress, Description: "later current task"},
	}), "owner-session")
	if data.Total != 3 || data.Done != 1 || data.Current == nil || data.Current.ID != 2 || data.Current.Description != "first current task" {
		t.Fatalf("taskUpdatedData() = %+v", data)
	}
	if data.TaskStoreOwnerSessionID != "owner-session" {
		t.Fatalf("taskUpdatedData() owner = %q, want owner-session", data.TaskStoreOwnerSessionID)
	}
}
