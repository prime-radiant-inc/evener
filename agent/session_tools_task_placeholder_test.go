package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// Some models fill every optional task_list field on every update: an empty
// depends_on, a null, or the string "null" as notes. Those placeholders must
// leave the task as it was; only [0] clears dependencies (#3879).

func newDependentTaskHarness(t *testing.T) *taskToolHarness {
	t.Helper()
	return newTaskToolHarness(t, []taskpkg.TaskInput{
		{Description: "first", Prompt: "p1"},
		{Description: "second", Prompt: "p2", DependsOn: []int{1}},
	})
}

func taskByID(t *testing.T, store *taskpkg.TaskStore, id int) taskpkg.Task {
	t.Helper()
	for _, task := range store.View() {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %d not found", id)
	return taskpkg.Task{}
}

func TestTaskTool_UpdatePlaceholderDependsOnKeepsDependencies(t *testing.T) {
	t.Parallel()
	for name, deps := range map[string]any{"empty": []any{}, "null": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			res := h.update(t, map[string]any{"id": 2, "notes": "decided to keep the order", "depends_on": deps})
			if res.IsError {
				t.Fatalf("update with placeholder depends_on: %s", res.FullOutput)
			}
			if got := taskByID(t, h.store, 2).DependsOn; !slices.Equal(got, []int{1}) {
				t.Fatalf("depends_on = %v, want [1] kept", got)
			}
		})
	}
}

func TestTaskTool_UpdateNullDependsOnPassesPreparation(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	registered := h.reg.Get("task_list")
	if registered == nil {
		t.Fatal("task_list not registered")
	}
	raw := json.RawMessage(`{"update":[{"id":2,"status":"cancelled","depends_on":null}]}`)
	prepared := prepareToolCall(llm.ToolCallData{ID: "c", Name: "task_list", Arguments: raw}, registered, []string{"task_list"}, "task_list", "communicate", "")
	if prepared.PrevalErr != "" {
		t.Fatalf("null depends_on rejected in preparation: %s", prepared.PrevalErr)
	}
}

func TestTaskTool_UpdateZeroDependsOnClearsDependencies(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	res := h.update(t, map[string]any{"id": 2, "depends_on": []any{0}})
	if res.IsError {
		t.Fatalf("[0] must clear dependencies: %s", res.FullOutput)
	}
	if got := taskByID(t, h.store, 2).DependsOn; len(got) != 0 {
		t.Fatalf("depends_on = %v, want cleared", got)
	}
}

func TestTaskTool_ZeroDependsOnMixedOrOnAddRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"update mixed": {
			args: map[string]any{"update": []any{map[string]any{"id": 2, "depends_on": []any{0, 1}}}},
			want: "[0] on its own clears",
		},
		"add": {
			args: map[string]any{"add": []any{map[string]any{"type": "implement", "description": "third", "prompt": "p3", "depends_on": []any{0}}}},
			want: "a new task has no dependencies to clear",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			res := h.call(t, tc.args)
			if !res.IsError {
				t.Fatalf("want rejection, got: %s", res.FullOutput)
			}
			if !strings.Contains(res.FullOutput, tc.want) {
				t.Fatalf("error should say %q: %s", tc.want, res.FullOutput)
			}
			if len(h.store.View()) != 2 || !slices.Equal(taskByID(t, h.store, 2).DependsOn, []int{1}) {
				t.Fatalf("rejected call changed the list: %+v", h.store.View())
			}
		})
	}
}

func TestTaskTool_UpdatePlaceholderNotesNotAppended(t *testing.T) {
	t.Parallel()
	for _, notes := range []string{"", "null", " NULL ", "Null"} {
		t.Run(notes, func(t *testing.T) {
			t.Parallel()
			h := newDependentTaskHarness(t)
			res := h.update(t, map[string]any{"id": 1, "status": "in_progress", "notes": notes})
			if res.IsError {
				t.Fatalf("status update with placeholder notes: %s", res.FullOutput)
			}
			if got := taskByID(t, h.store, 1).Notes; len(got) != 0 {
				t.Fatalf("notes = %q, want none appended", got)
			}
		})
	}
}

func TestTaskTool_UpdateRealNotesAppended(t *testing.T) {
	t.Parallel()
	h := newDependentTaskHarness(t)
	res := h.update(t, map[string]any{"id": 1, "notes": "null pointer in the parser was the cause"})
	if res.IsError {
		t.Fatalf("real notes: %s", res.FullOutput)
	}
	if got := taskByID(t, h.store, 1).Notes; !slices.Equal(got, []string{"null pointer in the parser was the cause"}) {
		t.Fatalf("notes = %q", got)
	}
}
