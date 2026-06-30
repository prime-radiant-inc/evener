package task

import (
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

func TestProgressCountsOnlyDone(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.Append([]TaskInput{{Description: "a"}, {Description: "b"}, {Description: "c"}})
	_ = s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}})
	_ = s.Update([]TaskUpdate{{ID: added[1].ID, Status: TaskCancelled}})
	total, done := s.Progress()
	if total != 3 || done != 1 {
		t.Errorf("Progress = (%d,%d), want (3,1) — cancelled is not done", total, done)
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

func TestCurrentInProgress(t *testing.T) {
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
