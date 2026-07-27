package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
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
		taskGuard: taskGuard{
			getOrCreateTaskStore: func() *taskpkg.TaskStore { return h.store },
			markUsed:             func() {},
			setReasoningEffort:   func(string) {},
		},
	}
	h.reg = tool.NewRegistry()
	registerTaskTools(h.reg, deps)
	return h
}

func (h *taskToolHarness) update(t *testing.T, updates ...map[string]any) tool.ExecResult {
	t.Helper()
	args, err := json.Marshal(map[string]any{"action": "update", "updates": updates})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	return h.reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID: "task-update", Name: "task_list", Arguments: args,
	})
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
