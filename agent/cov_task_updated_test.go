package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
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
		"add": []map[string]any{
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
		"update": []map[string]any{{"id": 1, "status": "done"}},
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
		"update": []map[string]any{{"id": 2, "status": "in_progress"}},
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

func TestTaskTool_MixedAddFailingUpdateIsAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "mixed-atomic")
	if _, err := store.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeImplement, Description: "current", Prompt: "p"},
		{Type: taskpkg.TaskTypeImplement, Description: "open", Prompt: "p"},
	}); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("seed current task: %v", err)
	}
	before := store.View()
	wantPersisted, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal pre-call tasks: %v", err)
	}

	var emitted []events.EventData
	deps := &toolDeps{
		emit:           func(_ events.EventKind, data events.EventData) { emitted = append(emitted, data) },
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return store },
			markUsed:             func() {},
		},
	}
	reg := tool.NewRegistry()
	registerTaskTools(reg, deps)

	// The add is valid, but the update would introduce a second current task.
	// It must fail as one transaction: no task, save, event, or next-ID advance
	// from the add may escape before the conflict is detected.
	args := json.RawMessage(`{"add":[{"type":"implement","description":"must not leak","prompt":"p"}],"update":[{"id":2,"status":"in_progress"}]}`)
	for _, callID := range []string{"failed", "retry-failed"} {
		res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: callID, Name: "task_list", Arguments: args})
		if !res.IsError || !strings.Contains(res.Output, "only one task may be in_progress") {
			t.Fatalf("mixed call %s = %+v, want in_progress conflict", callID, res)
		}
		if got := store.View(); !reflect.DeepEqual(got, before) {
			t.Fatalf("in-memory tasks after %s = %#v, want %#v", callID, got, before)
		}
		reloaded := taskpkg.NewTaskStore(dir, "mixed-atomic")
		if err := reloaded.Load(); err != nil {
			t.Fatalf("reload after %s: %v", callID, err)
		}
		persisted, err := json.Marshal(reloaded.View())
		if err != nil {
			t.Fatalf("marshal persisted tasks after %s: %v", callID, err)
		}
		if !bytes.Equal(persisted, wantPersisted) {
			t.Fatalf("persisted tasks after %s = %s, want %s", callID, persisted, wantPersisted)
		}
	}
	if len(emitted) != 0 {
		t.Fatalf("failed mixed calls emitted task updates: %#v", emitted)
	}

	// A corrected retry gets the original next ID, proving neither failed call
	// persisted a partial add or consumed an ID.
	corrected := json.RawMessage(`{"add":[{"type":"implement","description":"must not leak","prompt":"p"}],"update":[{"id":1,"status":"done"}]}`)
	res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{ID: "corrected", Name: "task_list", Arguments: corrected})
	if res.IsError {
		t.Fatalf("corrected mixed retry: %s", res.Output)
	}
	after := store.View()
	if len(after) != 3 || after[2].ID != 3 || after[2].Description != "must not leak" {
		t.Fatalf("corrected retry tasks = %#v, want exactly one new task with ID 3", after)
	}
}

func TestTaskUpdatedDataUsesFirstInProgressTask(t *testing.T) {
	data := taskUpdatedData(taskpkg.Summarize([]taskpkg.Task{
		{ID: 1, Status: taskpkg.TaskDone},
		{ID: 2, Status: taskpkg.TaskInProgress, Description: "first current task"},
		{ID: 3, Status: taskpkg.TaskInProgress, Description: "later current task"},
	}), "owner-session", 7, 9)
	if data.Total != 3 || data.Done != 1 || data.Current == nil || data.Current.ID != 2 || data.Current.Description != "first current task" {
		t.Fatalf("taskUpdatedData() = %+v", data)
	}
	if data.TaskStoreOwnerSessionID != "owner-session" {
		t.Fatalf("taskUpdatedData() owner = %q, want owner-session", data.TaskStoreOwnerSessionID)
	}
	if data.TaskPublicationRevision != 9 {
		t.Fatalf("taskUpdatedData() revision = %d, want 9", data.TaskPublicationRevision)
	}
	if data.TaskPublicationEpoch != 7 {
		t.Fatalf("taskUpdatedData() epoch = %d, want 7", data.TaskPublicationEpoch)
	}
}
