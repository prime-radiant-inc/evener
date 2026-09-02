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

type taskToolStateEntry struct {
	ID      int                `json:"id"`
	Status  taskpkg.TaskStatus `json:"status"`
	Started *bool              `json:"started,omitempty"`
}

type taskToolHarness struct {
	store   *taskpkg.TaskStore
	steers  []string
	emitted []events.EventData
	reg     *tool.Registry
}

func newTaskToolHarness(t *testing.T, inputs []taskpkg.TaskInput) *taskToolHarness {
	t.Helper()
	store := taskpkg.NewTaskStore(t.TempDir(), "task-tool")
	if _, err := store.Append(inputs); err != nil {
		t.Fatalf("append tasks: %v", err)
	}
	h := &taskToolHarness{store: store}
	deps := &toolDeps{
		emit: func(_ events.EventKind, data events.EventData) {
			h.emitted = append(h.emitted, data)
		},
		steer: func(text, _ string) {
			h.steers = append(h.steers, text)
		},
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return h.store },
			markUsed:             func() {},
		},
	}
	h.reg = tool.NewRegistry()
	registerTaskTools(h.reg, deps)
	return h
}

// call executes a task_list call with the given raw arguments (nil = bare
// view call). It replaces the old action-shaped update helper: presence of
// the add/update arrays is the whole dispatch.
func (h *taskToolHarness) call(t *testing.T, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal task_list args: %v", err)
	}
	return h.reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID: "task-call", Name: "task_list", Arguments: raw,
	})
}

// update is a convenience for update-only calls.
func (h *taskToolHarness) update(t *testing.T, updates ...map[string]any) tool.ExecResult {
	t.Helper()
	return h.call(t, map[string]any{"update": updates})
}

func decodeTaskToolState(t *testing.T, result tool.ExecResult) []taskToolStateEntry {
	t.Helper()
	if len(result.ToolState) == 0 {
		t.Fatal("task update returned no tool state")
	}
	var state []taskToolStateEntry
	if err := json.Unmarshal(result.ToolState, &state); err != nil {
		t.Fatalf("decode task tool state: %v; raw=%s", err, result.ToolState)
	}
	return state
}

func taskStateEntry(t *testing.T, state []taskToolStateEntry, id int) taskToolStateEntry {
	t.Helper()
	for _, entry := range state {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("task state has no task %d: %+v", id, state)
	return taskToolStateEntry{}
}

func TestTaskTool_UpdateNotesOnlyKeepsStatus(t *testing.T) {
	t.Parallel()
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "note-me", Type: "implement", Prompt: "do it"}})

	res := h.update(t, map[string]any{"id": 1, "notes": "progress note"})
	if res.Err != nil {
		t.Fatalf("notes-only update: %v", res.Err)
	}
	state := decodeTaskToolState(t, res)
	entry := taskStateEntry(t, state, 1)
	if entry.Status != taskpkg.TaskOpen {
		t.Fatalf("notes-only update changed status to %q, want open", entry.Status)
	}
	// A follow-up status-bearing update on the same task still works — the
	// empty-status sentinel must not wedge the task.
	res = h.update(t, map[string]any{"id": 1, "status": "done"})
	if res.Err != nil {
		t.Fatalf("status update after notes-only: %v", res.Err)
	}
	state = decodeTaskToolState(t, res)
	entry = taskStateEntry(t, state, 1)
	if entry.Status != taskpkg.TaskDone {
		t.Fatalf("status update did not apply: %q", entry.Status)
	}
}

func TestTaskTool_UpdateClassifiesStartsFromPreState(t *testing.T) {
	t.Run("notes-only reassertion is not a start", func(t *testing.T) {
		h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "investigate", Prompt: "inspect"}})
		if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
			t.Fatalf("start task: %v", err)
		}

		result := h.update(t, map[string]any{
			"id": 1, "status": "in_progress", "notes": "found the root cause",
		})
		if result.IsError {
			t.Fatalf("notes-only update failed: %s", result.Output)
		}
		if len(h.steers) != 0 {
			t.Fatalf("notes-only reassertion steered %d times: %q", len(h.steers), h.steers)
		}
		entry := taskStateEntry(t, decodeTaskToolState(t, result), 1)
		if entry.Started == nil || *entry.Started {
			t.Fatalf("reassertion marker = %v, want explicit false", entry.Started)
		}
	})

	t.Run("status transition into in_progress is a start", func(t *testing.T) {
		h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "implement", Prompt: "build"}})
		result := h.update(t, map[string]any{"id": 1, "status": "in_progress"})
		if result.IsError {
			t.Fatalf("start update failed: %s", result.Output)
		}
		if len(h.steers) != 1 || !strings.Contains(h.steers[0], "implement") {
			t.Fatalf("steering = %q, want one current-task message", h.steers)
		}
		entry := taskStateEntry(t, decodeTaskToolState(t, result), 1)
		if entry.Started == nil || !*entry.Started {
			t.Fatalf("transition marker = %v, want explicit true", entry.Started)
		}
	})

	t.Run("mixed completion and start classifies only the transition", func(t *testing.T) {
		h := newTaskToolHarness(t, []taskpkg.TaskInput{
			{Description: "finish", Prompt: "finish"},
			{Description: "continue", Prompt: "continue"},
		})
		if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
			t.Fatalf("start task: %v", err)
		}

		result := h.update(t,
			map[string]any{"id": 1, "status": "done"},
			map[string]any{"id": 2, "status": "in_progress"},
		)
		if result.IsError {
			t.Fatalf("mixed update failed: %s", result.Output)
		}
		if len(h.steers) != 1 || !strings.Contains(h.steers[0], "continue") {
			t.Fatalf("steering = %q, want one steering for task 2", h.steers)
		}
		state := decodeTaskToolState(t, result)
		if started := taskStateEntry(t, state, 2).Started; started == nil || !*started {
			t.Fatalf("task 2 marker = %v, want true", started)
		}
		if started := taskStateEntry(t, state, 1).Started; started != nil {
			t.Fatalf("task 1 marker = %v, want absent for final done status", started)
		}
	})
}

func TestTaskTool_UpdateReopenEmitsTaskUpdated(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "reopen", Prompt: "reopen"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	result := h.update(t, map[string]any{"id": 1, "status": "open"})
	if result.IsError {
		t.Fatalf("reopen update failed: %s", result.Output)
	}
	var found []events.TaskUpdatedData
	for _, data := range h.emitted {
		if taskUpdate, ok := data.(events.TaskUpdatedData); ok {
			found = append(found, taskUpdate)
		}
	}
	if len(found) != 1 {
		t.Fatalf("task-updated events = %+v, want one event for reopen", found)
	}
	if found[0].Total != 1 || found[0].Done != 0 {
		t.Fatalf("task-updated event = %+v, want total=1 done=0", found[0])
	}
}

func TestTaskTool_UpdateClassifiesDuplicateIDsFromFinalBatchState(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{
		{Description: "finish", Prompt: "finish"},
		{Description: "continue", Prompt: "continue"},
	})

	result := h.update(t,
		map[string]any{"id": 1, "status": "in_progress"},
		map[string]any{"id": 1, "status": "done"},
	)
	if result.IsError {
		t.Fatalf("duplicate-ID update failed: %s", result.Output)
	}
	if len(h.steers) != 1 || !strings.Contains(h.steers[0], "continue") {
		t.Fatalf("steering = %q, want auto-start for task 2 only", h.steers)
	}
	state := decodeTaskToolState(t, result)
	if got := taskStateEntry(t, state, 1); got.Status != taskpkg.TaskDone || got.Started != nil {
		t.Fatalf("task 1 state = %+v, want done without a start marker", got)
	}
	if got := taskStateEntry(t, state, 2); got.Status != taskpkg.TaskInProgress {
		t.Fatalf("task 2 state = %+v, want auto-started in_progress", got)
	}
}

func TestTaskTool_UpdateMarkerOnlyDescribesExplicitFinalInProgress(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "open", status: "open"},
		{name: "done", status: "done"},
		{name: "cancelled", status: "cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: tc.name, Prompt: tc.name}})
			if tc.status == "done" || tc.status == "cancelled" {
				if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
					t.Fatalf("start task: %v", err)
				}
			}
			result := h.update(t, map[string]any{"id": 1, "status": tc.status})
			if result.IsError {
				t.Fatalf("%s update failed: %s", tc.status, result.Output)
			}
			if got := taskStateEntry(t, decodeTaskToolState(t, result), 1); got.Started != nil {
				t.Fatalf("%s update marker = %v, want absent", tc.status, got.Started)
			}
		})
	}
}

func TestTaskTool_UpdateCompletionUsesLegacySteerFallback(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "finish", Prompt: "finish"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start task: %v", err)
	}

	result := h.update(t, map[string]any{"id": 1, "status": "done"})
	if result.IsError {
		t.Fatalf("done update failed: %s", result.Output)
	}
	if len(h.steers) != 1 {
		t.Fatalf("legacy completion steering count = %d, want 1", len(h.steers))
	}
	payload := parseTaskCompletionLLMPayload(t, llm.User(h.steers[0]))
	if payload.CompletionState != "ready_for_final_output" || len(payload.BlockingDelegateIDs) != 0 {
		t.Fatalf("legacy completion payload = %+v, want ready with no blocking delegates", payload)
	}
}

// TestTaskTool_CombinedAddUpdate: both arrays in one call apply atomically
// with one publication revision and one EventTaskUpdated. The update must
// target a PRE-EXISTING task — updates validate against the pre-add state
// (the model cannot know IDs this call's add would assign).
func TestTaskTool_CombinedAddUpdate(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "existing", Prompt: "p0"}})
	res := h.call(t, map[string]any{
		"add": []any{map[string]any{
			"type": "implement", "description": "first", "prompt": "do one",
		}},
		"update": []any{map[string]any{
			"id": 1, "status": "in_progress",
		}},
	})
	if res.IsError {
		t.Fatalf("combined call: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "Added 1 task(s); updated 1→in_progress") {
		t.Fatalf("output should combine add+update ack: %q", res.FullOutput)
	}
	view := h.store.View()
	if len(view) != 2 || view[0].Status != taskpkg.TaskInProgress || view[1].Status != taskpkg.TaskOpen {
		t.Fatalf("combined call should update existing and add new: %+v", view)
	}
}

// TestTaskTool_UpdateReferencesThisCallAddsRejected: the model cannot know
// IDs this call's add would assign, so updates must validate against the
// pre-add store state, and a failed combined call must apply nothing.
func TestTaskTool_UpdateReferencesThisCallAddsRejected(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.call(t, map[string]any{
		"add": []any{map[string]any{
			"type": "implement", "description": "first", "prompt": "do one",
		}},
		"update": []any{map[string]any{
			"id": 5, "status": "in_progress",
		}},
	})
	if !res.IsError {
		t.Fatal("update targeting an ID this call's add would create must be rejected")
	}
	if !strings.Contains(res.FullOutput, "unknown task ID 5") {
		t.Fatalf("error should name the unknown ID: %s", res.FullOutput)
	}
	if len(h.store.View()) != 0 {
		t.Fatal("failed combined call must not apply its adds either (atomicity)")
	}
}

// TestTaskTool_EmptyArraysAreNoOps: strict-mode models force-send both
// arrays; empty ones must be no-ops. With no mutation, the response is
// the view output (the list), same as a bare call.
func TestTaskTool_EmptyArraysAreNoOps(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.call(t, map[string]any{"add": []any{}, "update": []any{}})
	if res.IsError {
		t.Fatalf("empty arrays must be no-ops: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "1. [open]") {
		t.Fatalf("response must still return the list: %q", res.FullOutput)
	}
}

// TestTaskTool_ViewIsBareCall: no arrays = view, returns the list.
func TestTaskTool_ViewIsBareCall(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.call(t, map[string]any{})
	if res.IsError {
		t.Fatalf("bare call must be a view: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "1. [open]") {
		t.Fatalf("bare call must return the list: %q", res.FullOutput)
	}
}

// TestTaskTool_OldActionShapeRejectedHelpfully: back-compat — old
// action-shaped calls fail at validation. The call below deliberately
// sends the retired action key; do not "migrate" it.
func TestTaskTool_OldActionShapeRejectedHelpfully(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.call(t, map[string]any{"action": "view"})
	if !res.IsError {
		t.Fatal("old action-shaped call must be rejected")
	}
}

// TestTaskTool_NoOpUpdateEntryRejected: an update entry that changes
// nothing is a model mistake, not a no-op.
func TestTaskTool_NoOpUpdateEntryRejected(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	res := h.call(t, map[string]any{
		"update": []any{map[string]any{"id": 1}},
	})
	if !res.IsError {
		t.Fatal("empty update entry must be rejected")
	}
	if !strings.Contains(res.FullOutput, "changes nothing") {
		t.Fatalf("error should say the entry changes nothing: %s", res.FullOutput)
	}
}

// TestTaskTool_AutoAdvanceCanPickSameCallAdd: completing a task in a call
// that also adds an eligible replacement auto-starts the new task in the
// same publication — "when you mark a task done, the next eligible task
// auto-starts" applies to same-call adds too.
func TestTaskTool_AutoAdvanceCanPickSameCallAdd(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "first", Prompt: "p1"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	res := h.call(t, map[string]any{
		"update": []any{map[string]any{"id": 1, "status": "done"}},
		"add":    []any{map[string]any{"type": "implement", "description": "second", "prompt": "p2"}},
	})
	if res.IsError {
		t.Fatalf("combined completion+add: %s", res.FullOutput)
	}
	view := h.store.View()
	if len(view) != 2 || view[1].Status != taskpkg.TaskInProgress {
		t.Fatalf("auto-advance should start the same-call add: %+v", view)
	}
	if len(h.steers) == 0 {
		t.Fatal("auto-advance steering should fire for the new task")
	}
}

// TestTaskTool_NotesOnlyUpdateWorks: the end-to-end bug fix from the review.
func TestTaskTool_NotesOnlyUpdateWorks(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "d", Prompt: "p"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	res := h.call(t, map[string]any{
		"update": []any{map[string]any{"id": 1, "notes": "found the root cause"}},
	})
	if res.IsError {
		t.Fatalf("notes-only update must succeed (was: invalid status \"<nil>\"): %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "Updated 1.") {
		t.Fatalf("ack: %q", res.FullOutput)
	}
}

func TestTaskTool_RejectsUnknownNestedFieldsAtomically(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "add brief instead of description",
			args: map[string]any{"add": []any{map[string]any{
				"type": "implement", "brief": "wrong field", "prompt": "do it",
			}}},
			want: []string{"brief", "description", "type", "prompt"},
		},
		{
			name: "add missing description",
			args: map[string]any{"add": []any{map[string]any{
				"type": "implement", "prompt": "do it",
			}}},
			want: []string{"description", "type", "prompt"},
		},
		{
			name: "add unknown field alongside valid mutation",
			args: map[string]any{"add": []any{
				map[string]any{"type": "implement", "description": "valid", "prompt": "do it"},
				map[string]any{"type": "verify", "description": "invalid", "prompt": "check it", "unknown": true},
			}},
			want: []string{"unknown"},
		},
		{
			name: "malformed update alongside valid mutation",
			args: map[string]any{"update": []any{
				map[string]any{"id": 1, "status": "done"},
				map[string]any{"id": 1, "brief": "invalid"},
			}},
			want: []string{"brief", "description"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "existing", Prompt: "keep me"}})
			res := h.call(t, tc.args)
			if !res.IsError {
				t.Fatalf("nested unknown field must be rejected: %s", res.FullOutput)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.FullOutput, want) {
					t.Errorf("diagnostic = %q, want %q", res.FullOutput, want)
				}
			}
			view := h.store.View()
			if len(view) != 1 || view[0].Status != taskpkg.TaskOpen || view[0].Description != "existing" {
				t.Fatalf("invalid batch mutated tasks: %+v", view)
			}
		})
	}
}

func TestTaskTool_CancelledTerminalListUsesOutcomeSummary(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "stop", Prompt: "stop"}})
	res := h.update(t, map[string]any{"id": 1, "status": "cancelled"})
	if res.IsError {
		t.Fatalf("cancel task: %s", res.FullOutput)
	}
	for _, want := range []string{
		"No actionable tasks remain.",
		"0 done, 1 cancelled, 0 remaining (1 total)",
	} {
		if !strings.Contains(res.FullOutput, want) {
			t.Errorf("terminal response = %q, want %q", res.FullOutput, want)
		}
	}
	if strings.Contains(res.FullOutput, "All tasks complete") {
		t.Fatalf("cancelled terminal response must not say all tasks complete: %q", res.FullOutput)
	}
}

// TestTaskTool_UpdateDependsOnSameCallAddRejected: an update's depends_on
// may not reference an ID this call's add would assign — the model cannot
// know it, and allowing it invites ID-guessing (the same reason update
// targets validate against the pre-add state).
func TestTaskTool_UpdateDependsOnSameCallAddRejected(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "existing", Prompt: "p"}})
	res := h.call(t, map[string]any{
		"add": []any{map[string]any{
			"type": "implement", "description": "new", "prompt": "p",
		}},
		"update": []any{map[string]any{
			"id": 1, "depends_on": []any{2},
		}},
	})
	if !res.IsError {
		t.Fatalf("update depending on same-call add must be rejected, got: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "depends on unknown task 2") {
		t.Fatalf("error should name the unknown dep: %s", res.FullOutput)
	}
	if len(h.store.View()) != 1 {
		t.Fatal("rejected combined call must apply nothing (atomicity)")
	}
}
