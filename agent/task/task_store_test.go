package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// newTestStore returns a TaskStore on a temp path with a deterministic,
// monotonically-advancing clock so timestamp progression is observable.
func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	var n int64
	return NewTaskStore(t.TempDir(), "s").SetClock(func() time.Time {
		n++
		return time.Unix(1000+n, 0).UTC()
	})
}

func TestAppend_AssignsIDsStatusTypeAndStamps(t *testing.T) {
	s := newTestStore(t)
	added, err := s.Append([]TaskInput{
		{Description: "a", Prompt: "pa"}, // Type empty -> implement
		{Type: TaskTypeResearch, Description: "b", Prompt: "pb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 || added[0].ID != 1 || added[1].ID != 2 {
		t.Fatalf("added = %+v, want 2 tasks with IDs 1,2", added)
	}
	if added[0].Type != TaskTypeImplement {
		t.Errorf("default Type = %q, want implement", added[0].Type)
	}
	if added[1].Type != TaskTypeResearch {
		t.Errorf("Type = %q, want research", added[1].Type)
	}
	if added[0].Status != TaskOpen {
		t.Errorf("Status = %q, want open", added[0].Status)
	}
	if added[0].CreatedAt == nil || added[0].UpdatedAt == nil {
		t.Error("CreatedAt/UpdatedAt not stamped")
	}
}

func TestLoad_NormalizesPersistedTaskEffortsWithoutChangingTaskState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks", "s.json")
	legacy := []Task{
		{ID: 7, Description: "invalid", Prompt: "recover", Status: TaskInProgress, DependsOn: []int{3}, ReasoningEffort: " ultra "},
		{ID: 3, Description: "valid", Prompt: "keep", Status: TaskDone, ReasoningEffort: " HIGH "},
		{ID: 9, Description: "empty", Prompt: "inherit", Status: TaskOpen},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewTaskStore(dir, "s")
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := store.View()
	if len(got) != len(legacy) {
		t.Fatalf("loaded %d tasks, want %d", len(got), len(legacy))
	}
	if got[0].ID != 7 || got[0].Status != TaskInProgress || got[0].ReasoningEffort != "" || len(got[0].DependsOn) != 1 || got[0].DependsOn[0] != 3 {
		t.Fatalf("invalid task was not migrated without losing identity/state: %+v", got[0])
	}
	if got[1].ID != 3 || got[1].Status != TaskDone || got[1].ReasoningEffort != "high" {
		t.Fatalf("valid task = %+v, want canonical high override", got[1])
	}
	if got[2].ReasoningEffort != "" {
		t.Fatalf("empty effort = %q, want inherit representation", got[2].ReasoningEffort)
	}

	// The migrated store remains usable, and a subsequent atomic save must not
	// reintroduce the stale value.
	if _, err := store.Append([]TaskInput{{Description: "after restore", Prompt: "continue"}}); err != nil {
		t.Fatalf("Append after migration: %v", err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(persisted, data) || len(persisted) == 0 {
		t.Fatalf("save did not rewrite migrated task state: %s", persisted)
	}
	var saved []Task
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatal(err)
	}
	if saved[0].ReasoningEffort != "" || saved[1].ReasoningEffort != "high" {
		t.Fatalf("saved migrated efforts = [%q, %q], want [empty, high]", saved[0].ReasoningEffort, saved[1].ReasoningEffort)
	}
}

func TestLoad_MalformedJSONDoesNotPartiallyReplaceStore(t *testing.T) {
	dir := t.TempDir()
	store := NewTaskStore(dir, "s")
	added, err := store.Append([]TaskInput{{Description: "existing", Prompt: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tasks", "s.json")
	if err := os.WriteFile(path, []byte(`[{"id":99,"description":"partial"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err == nil {
		t.Fatal("Load malformed JSON succeeded")
	}
	got := store.View()
	if len(got) != 1 || got[0].ID != added[0].ID || got[0].Description != "existing" {
		t.Fatalf("malformed Load partially replaced store: %+v", got)
	}
}

func TestAppend_UnknownDependencyRollsBackNextID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Append([]TaskInput{{Description: "x", DependsOn: []int{99}}}); err == nil {
		t.Fatal("unknown dependency: want error")
	}
	// The failed batch must not consume the ID.
	added, err := s.Append([]TaskInput{{Description: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].ID != 1 {
		t.Errorf("ID = %d after rollback, want 1", added[0].ID)
	}
}

func TestAppend_SelfDependencyAndCycleRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Append([]TaskInput{{Description: "a", DependsOn: []int{1}}}); err == nil {
		t.Error("self-dependency: want error")
	}
	// Two tasks in one batch depending on each other (IDs 1 and 2) form a cycle.
	if _, err := s.Append([]TaskInput{
		{Description: "a", DependsOn: []int{2}},
		{Description: "b", DependsOn: []int{1}},
	}); err == nil {
		t.Error("dependency cycle: want error")
	}
}

func TestUpdate_StatusNotesTimestampsAndCompletion(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}})
	id := added[0].ID

	if err := s.Update([]TaskUpdate{{ID: id, Status: TaskDone, Notes: "finished"}}); err != nil {
		t.Fatal(err)
	}
	got := s.View()[0]
	if got.Status != TaskDone {
		t.Errorf("Status = %q, want done", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt not stamped on done")
	}
	if !got.UpdatedAt.After(*got.CreatedAt) {
		t.Errorf("UpdatedAt %v not after CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
	if len(got.Notes) != 1 || got.Notes[0] != "finished" {
		t.Errorf("Notes = %v, want [finished]", got.Notes)
	}

	// Reopening a done task clears CompletedAt.
	if err := s.Update([]TaskUpdate{{ID: id, Status: TaskOpen}}); err != nil {
		t.Fatal(err)
	}
	if s.View()[0].CompletedAt != nil {
		t.Error("CompletedAt not cleared on reopen")
	}
}

func TestUpdateWithSnapshotReturnsAtomicPreAndPostStates(t *testing.T) {
	s := newTestStore(t)
	added, err := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.UpdateWithSnapshot([]TaskUpdate{
		{ID: added[0].ID, Status: TaskInProgress},
		{ID: added[0].ID, Status: TaskDone},
	})
	if err != nil {
		t.Fatalf("UpdateWithSnapshot: %v", err)
	}
	if got := snapshot.Before[0].Status; got != TaskOpen {
		t.Fatalf("before task 1 status = %q, want open", got)
	}
	if got := snapshot.After[0].Status; got != TaskDone {
		t.Fatalf("after task 1 status = %q, want done", got)
	}
	if got := s.View()[0].Status; got != TaskDone {
		t.Fatalf("store task 1 status = %q, want done", got)
	}
}

func TestUpdateWithSnapshotClonesTimestampPointers(t *testing.T) {
	s := newTestStore(t)
	added, err := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateWithSnapshot([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}}); err != nil {
		t.Fatalf("UpdateWithSnapshot: %v", err)
	}

	type timestampValues struct {
		createdAt   *time.Time
		updatedAt   *time.Time
		completedAt *time.Time
	}
	copyTime := func(value *time.Time) *time.Time {
		if value == nil {
			return nil
		}
		clonedValue := *value
		return &clonedValue
	}
	snapshot, err := s.UpdateWithSnapshot([]TaskUpdate{{ID: added[1].ID, Status: TaskOpen}})
	if err != nil {
		t.Fatalf("UpdateWithSnapshot: %v", err)
	}
	if snapshot.Before[0].CompletedAt == nil {
		// The completed task proves a non-nil CompletedAt pointer is covered;
		// task 2's nil CompletedAt values prove nil timestamps remain nil.
		t.Fatal("snapshot.Before[0].CompletedAt = nil, want a completed timestamp")
	}
	storeView := s.View()
	want := make([]timestampValues, len(storeView))
	for i, task := range storeView {
		want[i] = timestampValues{
			createdAt:   copyTime(task.CreatedAt),
			updatedAt:   copyTime(task.UpdatedAt),
			completedAt: copyTime(task.CompletedAt),
		}
	}

	mutated := 0
	for _, tasks := range [2][]Task{snapshot.Before, snapshot.After} {
		for i := range tasks {
			for _, timestamp := range []*time.Time{
				tasks[i].CreatedAt,
				tasks[i].UpdatedAt,
				tasks[i].CompletedAt,
			} {
				if timestamp == nil {
					continue
				}
				mutated++
				*timestamp = time.Unix(int64(9000+mutated), 0).UTC()
			}
		}
	}
	if mutated != 10 {
		t.Fatalf("mutated %d timestamp pointers, want 10", mutated)
	}

	got := s.View()
	for i, task := range got {
		for name, timestamps := range map[string][2]*time.Time{
			"CreatedAt":   {task.CreatedAt, want[i].createdAt},
			"UpdatedAt":   {task.UpdatedAt, want[i].updatedAt},
			"CompletedAt": {task.CompletedAt, want[i].completedAt},
		} {
			actual, expected := timestamps[0], timestamps[1]
			if (actual == nil) != (expected == nil) {
				t.Errorf("task %d %s nil = %t, want %t", task.ID, name, actual == nil, expected == nil)
				continue
			}
			if actual != nil && !actual.Equal(*expected) {
				t.Errorf("task %d %s = %v, want %v", task.ID, name, *actual, *expected)
			}
		}
	}
}

func TestUpdate_DependsOnChangeAndClear(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}})
	a, b := added[0].ID, added[1].ID

	// Set b to depend on a.
	if err := s.Update([]TaskUpdate{{ID: b, Status: TaskOpen, DependsOn: &[]int{a}}}); err != nil {
		t.Fatal(err)
	}
	if deps := s.View()[1].DependsOn; len(deps) != 1 || deps[0] != a {
		t.Fatalf("DependsOn = %v, want [%d]", deps, a)
	}
	// Clear with &[]int{}; nil would mean "no change".
	if err := s.Update([]TaskUpdate{{ID: b, Status: TaskOpen, DependsOn: &[]int{}}}); err != nil {
		t.Fatal(err)
	}
	if deps := s.View()[1].DependsOn; len(deps) != 0 {
		t.Errorf("DependsOn = %v, want cleared", deps)
	}
}

func TestUpdate_RejectsInvalidStatusUnknownIDAndDoubleInProgress(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}})
	a, b := added[0].ID, added[1].ID

	if err := s.Update([]TaskUpdate{{ID: a, Status: "bogus"}}); err == nil {
		t.Error("invalid status: want error")
	}
	if err := s.Update([]TaskUpdate{{ID: 999, Status: TaskOpen}}); err == nil {
		t.Error("unknown ID: want error")
	}
	// Two tasks in_progress in one batch violates the single-in_progress invariant.
	if err := s.Update([]TaskUpdate{
		{ID: a, Status: TaskInProgress},
		{ID: b, Status: TaskInProgress},
	}); err == nil {
		t.Error("two in_progress: want error")
	}
	// Neither half-applied: both still open.
	for _, tk := range s.View() {
		if tk.Status != TaskOpen {
			t.Fatalf("task %d = %q after rejected batch, want open", tk.ID, tk.Status)
		}
	}
}

func TestUpdate_DoubleInProgress_NamesTheBlockingTask(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "first task"}, {Description: "second task"}})
	first, second := added[0].ID, added[1].ID

	if err := s.Update([]TaskUpdate{{ID: first, Status: TaskInProgress}}); err != nil {
		t.Fatalf("starting first task: %v", err)
	}
	err := s.Update([]TaskUpdate{{ID: second, Status: TaskInProgress}})
	if err == nil {
		t.Fatal("second task in_progress while first still in_progress: want error")
	}
	want := fmt.Sprintf("only one task may be in_progress; %d %q is currently in_progress — complete or defer it in the same updates array", first, "first task")
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestUpdate_DoubleInProgress_DoesNotBlameATaskTheBatchResolves(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "first"}, {Description: "second"}, {Description: "third"}})
	first, second, third := added[0].ID, added[1].ID, added[2].ID

	if err := s.Update([]TaskUpdate{{ID: first, Status: TaskInProgress}}); err != nil {
		t.Fatalf("starting first task: %v", err)
	}

	// This batch resolves the old blocker (first -> done) but creates a NEW
	// conflict between second and third. The error must not blame first,
	// since first won't be in_progress once this batch applies.
	err := s.Update([]TaskUpdate{
		{ID: first, Status: TaskDone},
		{ID: second, Status: TaskInProgress},
		{ID: third, Status: TaskInProgress},
	})
	if err == nil {
		t.Fatal("second and third both in_progress in same batch: want error")
	}
	want := "only one task may be in_progress at a time; update would result in 2"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestSummarizeCountsOutcomesAndSelectsFirstInProgress(t *testing.T) {
	tasks := []Task{
		{ID: 1, Status: TaskDone, Description: "done"},
		{ID: 2, Status: TaskInProgress, Description: "first current"},
		{ID: 3, Status: TaskInProgress, Description: "later current"},
		{ID: 4, Status: TaskCancelled, Description: "cancelled"},
		{ID: 5, Status: TaskOpen, Description: "open"},
	}

	summary := Summarize(tasks)
	if summary.Total != 5 || summary.Done != 1 || summary.Cancelled != 1 || summary.Remaining != 3 {
		t.Fatalf("Summarize = %+v, want Total=5 Done=1 Cancelled=1 Remaining=3", summary)
	}
	if summary.Current == nil || summary.Current.ID != 2 || summary.Current.Description != "first current" {
		t.Fatalf("Summarize Current = %+v, want task 2 first current", summary.Current)
	}
}

func TestListSummaryProgressText(t *testing.T) {
	tests := []struct {
		name  string
		tasks []Task
		want  string
	}{
		{name: "empty", want: "0 done, 0 cancelled, 0 remaining (0 total)"},
		{name: "open", tasks: []Task{{Status: TaskOpen}}, want: "0 done, 0 cancelled, 1 remaining (1 total)"},
		{name: "in progress", tasks: []Task{{Status: TaskInProgress}}, want: "0 done, 0 cancelled, 1 remaining (1 total)"},
		{name: "done", tasks: []Task{{Status: TaskDone}}, want: "1 done, 0 cancelled, 0 remaining (1 total)"},
		{name: "cancelled", tasks: []Task{{Status: TaskCancelled}}, want: "0 done, 1 cancelled, 0 remaining (1 total)"},
		{name: "mixed", tasks: []Task{{Status: TaskDone}, {Status: TaskCancelled}, {Status: TaskOpen}, {Status: TaskInProgress}}, want: "1 done, 1 cancelled, 2 remaining (4 total)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Summarize(tc.tasks).ProgressText(); got != tc.want {
				t.Fatalf("ProgressText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeReturnsOwnedCurrentTask(t *testing.T) {
	tasks := []Task{{
		ID:          7,
		Description: "original",
		Status:      TaskInProgress,
		DependsOn:   []int{1, 2},
		Notes:       []string{"original note"},
	}}

	summary := Summarize(tasks)
	tasks[0].Description = "mutated"
	tasks[0].DependsOn[0] = 99
	tasks[0].Notes[0] = "mutated note"

	want := &Task{
		ID:          7,
		Description: "original",
		Status:      TaskInProgress,
		DependsOn:   []int{1, 2},
		Notes:       []string{"original note"},
	}
	if !reflect.DeepEqual(summary.Current, want) {
		t.Fatalf("Summarize Current = %+v after input mutation, want owned copy %+v", summary.Current, want)
	}
}

func TestTaskStore_ProgressReturnsDistinctOutcomeCounts(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}, {Description: "c"}})
	_ = s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}})
	_ = s.Update([]TaskUpdate{{ID: added[1].ID, Status: TaskCancelled}})
	total, done, cancelled, remaining := s.Progress()
	if total != 3 || done != 1 || cancelled != 1 || remaining != 1 {
		t.Errorf("Progress = (%d,%d,%d,%d), want (3,1,1,1)", total, done, cancelled, remaining)
	}
	summary := Summarize(s.View())
	if total != summary.Total || done != summary.Done || cancelled != summary.Cancelled || remaining != summary.Remaining {
		t.Errorf("Progress = (%d,%d,%d,%d), Summarize = (%d,%d,%d,%d)", total, done, cancelled, remaining, summary.Total, summary.Done, summary.Cancelled, summary.Remaining)
	}
}

func TestNextEligible_GatedByDependencies(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}})
	a := added[0].ID
	added2, _ := s.Append([]TaskInput{{Description: "b", DependsOn: []int{a}}})
	b := added2[0].ID

	// b depends on open a -> only a is eligible.
	elig := s.NextEligible()
	if len(elig) != 1 || elig[0].ID != a {
		t.Fatalf("eligible = %+v, want only task %d", elig, a)
	}
	// Complete a -> b becomes eligible.
	_ = s.Update([]TaskUpdate{{ID: a, Status: TaskDone}})
	elig = s.NextEligible()
	if len(elig) != 1 || elig[0].ID != b {
		t.Fatalf("eligible = %+v, want only task %d after a done", elig, b)
	}
}

func TestTaskStore_CurrentInProgress(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}})
	if _, ok := s.CurrentInProgress(); ok {
		t.Error("no task in progress yet, want ok=false")
	}
	_ = s.Update([]TaskUpdate{{ID: added[1].ID, Status: TaskInProgress}})
	cur, ok := s.CurrentInProgress()
	if !ok || cur.ID != added[1].ID {
		t.Errorf("CurrentInProgress = (%+v,%v), want task %d", cur, ok, added[1].ID)
	}
	summary := Summarize(s.View())
	if summary.Current == nil || !reflect.DeepEqual(cur, *summary.Current) {
		t.Errorf("CurrentInProgress = %+v, Summarize Current = %+v", cur, summary.Current)
	}
}

func TestPopulateFromTemplates(t *testing.T) {
	s := newTestStore(t)
	err := s.PopulateFromTemplates([]TaskTemplate{
		{Title: "first", Prompt: "p1"},
		{Title: "second", Prompt: "p2", Type: "verify"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks := s.View()
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if tasks[0].Status != TaskInProgress {
		t.Errorf("first task = %q, want auto-started in_progress", tasks[0].Status)
	}
	if tasks[1].Type != TaskTypeVerify {
		t.Errorf("second Type = %q, want verify", tasks[1].Type)
	}

	// Idempotent: a second populate on a non-empty store is a no-op.
	if err := s.PopulateFromTemplates([]TaskTemplate{{Title: "ignored"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.View()) != 2 {
		t.Errorf("tasks = %d after re-populate, want unchanged 2", len(s.View()))
	}
}

func TestPopulateFromTemplates_ParentTasksInsertExpands(t *testing.T) {
	s := newTestStore(t)
	err := s.PopulateFromTemplates(
		[]TaskTemplate{
			{Title: "lead", Prompt: "p"},
			{Insert: "parent_tasks"}, // replaced by the parent tasks
		},
		[]TaskTemplate{{Title: "from-parent-1"}, {Title: "from-parent-2"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := s.View()
	if len(got) != 3 {
		t.Fatalf("tasks = %d, want 3 (lead + 2 parent)", len(got))
	}
	if got[1].Description != "from-parent-1" || got[2].Description != "from-parent-2" {
		t.Errorf("parent_tasks not expanded: %q,%q", got[1].Description, got[2].Description)
	}
}

func TestView_ReturnsCopy(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Append([]TaskInput{{Description: "a"}})
	v := s.View()
	v[0].Description = "mutated"
	if s.View()[0].Description != "a" {
		t.Error("View did not return a copy; mutation leaked into the store")
	}
}

// TestTaskUpdate_EmptyStatusMeansNoChange pins the combined-tool contract:
// an update entry with an empty status leaves the task's status unchanged
// while still applying notes, deps, and effort — the tool schema has always
// documented status as optional, so the store must honor that.
func TestTaskUpdate_EmptyStatusMeansNoChange(t *testing.T) {
	s := newTestStore(t)
	added, err := s.Append([]TaskInput{{Description: "d", Prompt: "p"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Notes: "note one"}}); err != nil {
		t.Fatalf("notes-only update: %v", err)
	}
	got := s.View()
	if got[0].Status != TaskOpen {
		t.Fatalf("status changed by notes-only update: %v", got[0].Status)
	}
	if len(got[0].Notes) != 1 || got[0].Notes[0] != "note one" {
		t.Fatalf("notes not applied: %v", got[0].Notes)
	}

	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskInProgress}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Notes: "note two"}}); err != nil {
		t.Fatalf("notes-only during progress: %v", err)
	}
	got = s.View()
	if got[0].Status != TaskInProgress {
		t.Fatalf("notes-only update clobbered in_progress: %v", got[0].Status)
	}
	if len(got[0].Notes) != 2 {
		t.Fatalf("second note not appended: %v", got[0].Notes)
	}
}

// TestTaskUpdate_EmptyStatusStillValidates pins that empty status does not
// weaken the other validations: unknown IDs, unknown deps, and invalid
// non-empty statuses still fail.
func TestTaskUpdate_EmptyStatusStillValidates(t *testing.T) {
	s := newTestStore(t)
	added, err := s.Append([]TaskInput{{Description: "d", Prompt: "p"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: 999, Notes: "x"}}); err == nil {
		t.Fatal("unknown ID with empty status must still be rejected")
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, DependsOn: &[]int{999}}}); err == nil {
		t.Fatal("unknown dep must still be rejected")
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: "bogus"}}); err == nil {
		t.Fatal("bogus status must still be rejected")
	}
}
