package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	taskpkg "primeradiant.com/evener/agent/task"
)

// Invariant battery for the presence-based task_list handler: one test per
// contract invariant, each seeded to expose exactly that invariant.
func TestTaskTool_SingleInProgressUnderCombinedBatch(t *testing.T) {
	// Two pre-existing tasks; try to start both in one combined call.
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "a", Prompt: "p"}, {Description: "b", Prompt: "p"}})
	res := h.call(t, map[string]any{
		"update": []any{
			map[string]any{"id": 1, "status": "in_progress"},
			map[string]any{"id": 2, "status": "in_progress"},
		},
	})
	if !res.IsError {
		t.Fatal("two in_progress in one batch must be rejected")
	}
}

func TestTaskTool_AutoAdvanceFiresOnceWithSteering(t *testing.T) {
	h := newTaskToolHarness(t, []taskpkg.TaskInput{{Description: "a", Prompt: "p"}, {Description: "b", Prompt: "p"}})
	if err := h.store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatal(err)
	}
	steersBefore := len(h.steers)
	res := h.call(t, map[string]any{
		"update": []any{map[string]any{"id": 1, "status": "done"}},
	})
	if res.IsError {
		t.Fatalf("completion failed: %s", res.FullOutput)
	}
	// Auto-advance starts task 2: exactly one current-task steer.
	if got := len(h.steers) - steersBefore; got != 1 {
		t.Fatalf("auto-advance steering fired %d times, want 1", got)
	}
	if h.store.View()[1].Status != taskpkg.TaskInProgress {
		t.Fatal("auto-advance did not start task 2")
	}
}

func TestTaskTool_EventsPayloadShapeUnchanged(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	h.call(t, map[string]any{
		"add": []any{map[string]any{"type": "implement", "description": "x", "prompt": "p"}},
	})
	var found *events.TaskUpdatedData
	for _, d := range h.emitted {
		if td, ok := d.(events.TaskUpdatedData); ok {
			found = &td
		}
	}
	if found == nil {
		t.Fatal("add did not emit EventTaskUpdated")
	}
	if found.Total != 1 || found.Done != 0 {
		t.Fatalf("payload summary = %d/%d, want 1/0", found.Done, found.Total)
	}
	if found.TaskPublicationRevision == 0 {
		t.Fatal("payload missing publication revision")
	}
}

func TestTaskTool_AddDepOnUnknownRejected(t *testing.T) {
	h := newTaskToolHarness(t, nil)
	res := h.call(t, map[string]any{
		"add": []any{map[string]any{"type": "implement", "description": "x", "prompt": "p", "depends_on": []any{99}}},
	})
	if !res.IsError {
		t.Fatal("add depending on unknown ID must be rejected")
	}
	if len(h.store.View()) != 0 {
		t.Fatal("rejected add must not apply")
	}
}

func TestTaskTool_SameBatchAddDepsAllowed(t *testing.T) {
	// Intra-batch: second add may depend on the first add in the same call
	// (store validates against existing + pending).
	h := newTaskToolHarness(t, nil)
	res := h.call(t, map[string]any{
		"add": []any{
			map[string]any{"type": "implement", "description": "first", "prompt": "p"},
			map[string]any{"type": "verify", "description": "second", "prompt": "p", "depends_on": []any{1}},
		},
	})
	if res.IsError {
		t.Fatalf("same-batch dep must be allowed: %s", res.FullOutput)
	}
	view := h.store.View()
	if len(view) != 2 || len(view[1].DependsOn) != 1 || view[1].DependsOn[0] != 1 {
		t.Fatalf("same-batch dep not applied: %+v", view)
	}
}
