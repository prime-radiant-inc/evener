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

// Issue #626 regression: a task_list call with action "update" whose update
// entries were placed in the append-branch "tasks" array (the exact argument
// shape observed 17 times in session 034FsgXdyimiBvbubPlB4w) must be
// rejected with an error that names the append branch the tasks[0]
// requirement belongs to and points the caller at the "updates" array — not
// the bare append-branch required list, which read as describing the update
// call itself and sent the model into an unrecoverable retry loop. This
// exercises the real prevalidation seam (prepareToolCall → Schema.Validate →
// repair → ExplainSchemaError) with the real DefTaskList schema.
func TestPrepareToolCall_TaskListUpdateCallerNamesAppendBranch(t *testing.T) {
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefTaskList(nil))); err != nil {
		t.Fatalf("register task_list: %v", err)
	}
	rt := reg.Get("task_list")

	// Exact shape from the live session: update entries in "tasks", including
	// the fields the model added while following the old misdirecting error.
	call := llm.ToolCallData{
		ID:   "issue626",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action":"update","tasks":[{` +
			`"depends_on":[],"id":1,"notes":"","reasoning_effort":"inherit",` +
			`"status":"in_progress","type":"implement"}]}`),
	}
	res := prepareToolCall(call, rt, []string{"task_list"}, "task_list", "")
	if res.PrevalErr == "" {
		t.Fatalf("expected prevalidation failure for update-shaped entries in tasks, got none (changes: %v)", res.Changes)
	}
	if !strings.Contains(res.PrevalErr, `for action "append"`) {
		t.Fatalf("error must name the append branch the tasks[0] requirement belongs to: %q", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, `takes "updates"`) {
		t.Fatalf("error must point an update caller at the updates array: %q", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, `"updates": [{`) {
		t.Fatalf("error example must show the updates-array item shape: %q", res.PrevalErr)
	}
}

// The same call through execTool must surface the branch-named error to the
// model, and must not reach the handler's "update requires a non-empty
// 'updates' array" — that handler error was the other half of the loop.
func TestExecTool_TaskListUpdateCallerBranchNamed(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()
	registerTaskTools(s.reg, &toolDeps{
		emit:           func(events.EventKind, events.EventData) {},
		steer:          func(string, string) {},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore {
				return taskpkg.NewTaskStore(t.TempDir(), "issue626")
			},
			markUsed: func() {},
		},
	})
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "issue626-exec",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action":"update","tasks":[{"id":1,"status":"in_progress"}]}`),
	}, "")
	if !res.IsError {
		t.Fatalf("expected error for update-shaped entries in tasks, got: %s", res.FullOutput)
	}
	if strings.Contains(res.FullOutput, "update requires a non-empty") {
		t.Fatalf("error fell through to the handler's shape complaint instead of the branch-named schema error: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, `for action "append"`) {
		t.Fatalf("model-visible error must name the append branch: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, `takes "updates"`) {
		t.Fatalf("model-visible error must point the caller at updates: %s", res.FullOutput)
	}
}
