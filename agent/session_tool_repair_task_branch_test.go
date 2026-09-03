package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// The issue #626 failure family (update entries sent under the append
// branch's "tasks" array, observed 17 times in a live session) is now
// structurally unrepresentable: task_list has no action enum and no
// tasks/updates arrays, so the old malformed shape cannot be formed. These
// tests replace the #626 regression tests: they pin that the REMAINING
// wrong-shape failure modes — old action-shaped calls and update entries in
// the add array — fail with errors the repair layer can explain against the
// new schema, through the real prevalidation seam.
func TestPrepareToolCall_OldTaskListActionShapeRejected(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList(nil))); err != nil {
		t.Fatalf("register task_list: %v", err)
	}
	rt := reg.Get("task_list")

	call := llm.ToolCallData{
		ID:   "old-shape",
		Name: "task_list",
		// The retired action key, exactly as an old session (or a model that
		// learned the pre-rework contract) would send it.
		Arguments: json.RawMessage(`{"action":"update","updates":[{"id":1,"status":"done"}]}`),
	}
	res := prepareToolCall(call, rt, []string{"task_list"}, "task_list", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("expected prevalidation failure for action-shaped call, got none (changes: %v)", res.Changes)
	}
	if !strings.Contains(res.PrevalErr, "action") {
		t.Fatalf("error must name the retired action argument: %q", res.PrevalErr)
	}
	if res.Boundary != "retired_task_shape" {
		t.Fatalf("boundary = %q, want retired_task_shape", res.Boundary)
	}
}

// An update entry misfiled into the add array must fail validation with an
// error that names the add item's requirements — the same explainability the
// #626 fix established, carried to the new schema.
func TestPrepareToolCall_UpdateEntryInAddArrayExplains(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList(nil))); err != nil {
		t.Fatalf("register task_list: %v", err)
	}
	rt := reg.Get("task_list")

	call := llm.ToolCallData{
		ID:   "misfiled",
		Name: "task_list",
		// An update-shaped entry (id + status) inside add, where the schema
		// demands type/description/prompt.
		Arguments: json.RawMessage(`{"add":[{"id":1,"status":"in_progress"}]}`),
	}
	res := prepareToolCall(call, rt, []string{"task_list"}, "task_list", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("expected prevalidation failure for update entry in add, got none (changes: %v)", res.Changes)
	}
	if !strings.Contains(res.PrevalErr, "add") || !strings.Contains(res.PrevalErr, "description") {
		t.Fatalf("error must name the add item requirements: %q", res.PrevalErr)
	}
}

// The same misfiled-entry call through execTool must surface the targeted
// task-list diagnostic before generic schema repair can discard its fields.
func TestExecTool_TaskListMisfiledEntrySurfacesTargetedPrevalidation(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()
	registerTaskTools(s.reg, &toolDeps{
		emit:           func(events.EventKind, events.EventData) {},
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore {
				return taskpkg.NewTaskStore(t.TempDir(), "misfiled")
			},
			markUsed: func() {},
		},
	})
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "misfiled-exec",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"add":[{"id":1,"status":"in_progress"}]}`),
	}, "")
	if !res.IsError {
		t.Fatalf("expected error for update-shaped entry in add, got: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "add entry 0") || !strings.Contains(res.FullOutput, "id") || !strings.Contains(res.FullOutput, "status") {
		t.Fatalf("model-visible error must name the misfiled add fields: %s", res.FullOutput)
	}
}

func TestExecTool_TaskListBriefSurfacesTargetedPrevalidation(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()
	registerTaskTools(s.reg, &toolDeps{
		emit:           func(events.EventKind, events.EventData) {},
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore {
				return taskpkg.NewTaskStore(t.TempDir(), "brief-prevalidation")
			},
			markUsed: func() {},
		},
	})
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "brief-exec",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"add":[{"type":"implement","description":"write test","prompt":"write test","brief":"wrong field"}]}`),
	}, "")
	if !res.IsError {
		t.Fatalf("expected targeted brief validation error, got: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "brief") || !strings.Contains(res.FullOutput, "required field named") || !strings.Contains(res.FullOutput, "description") {
		t.Fatalf("model-visible error = %q, want targeted brief guidance", res.FullOutput)
	}
}
