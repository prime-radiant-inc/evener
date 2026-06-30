package task

import "testing"

// TestUpdate_AtomicWhenDependencyValidationFails guards the fix for the
// non-atomic Update found by FuzzTaskStore: a batch whose later update carries a
// bad dependency used to apply earlier status mutations to the in-memory store
// before failing, leaving it corrupted (e.g. two tasks in_progress). Update now
// validates the projected dependency graph up front, so a rejected batch leaves
// the store exactly as it was.
func TestUpdate_AtomicWhenDependencyValidationFails(t *testing.T) {
	s := newTestStore(t)
	s.Append([]TaskInput{{Description: "a"}, {Description: "b"}}) //nolint:errcheck
	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}}); err != nil {
		t.Fatal(err)
	}

	// Unknown dependency in a later update — would previously half-apply the batch.
	bad := []int{99}
	err := s.Update([]TaskUpdate{
		{ID: 2, Status: TaskInProgress, DependsOn: &bad},
		{ID: 1, Status: TaskOpen},
	})
	if err == nil {
		t.Fatal("expected an unknown-dependency error")
	}
	inProgress := 0
	for _, tk := range s.View() {
		if tk.Status == TaskInProgress {
			inProgress++
		}
	}
	if inProgress != 1 {
		t.Fatalf("after a rejected batch, %d tasks in_progress, want 1 (state must be unchanged)", inProgress)
	}
	if s.View()[1].Status == TaskInProgress {
		t.Error("task 2 left in_progress despite the batch being rejected")
	}
	if s.View()[0].Status != TaskInProgress {
		t.Error("task 1's status changed despite the batch being rejected")
	}

	// A dependency cycle formed jointly by two updates in one batch must also be
	// rejected atomically (the per-update check could miss the cross-update cycle).
	s2 := newTestStore(t)
	s2.Append([]TaskInput{{Description: "a"}, {Description: "b"}}) //nolint:errcheck
	if err := s2.Update([]TaskUpdate{
		{ID: 1, Status: TaskOpen, DependsOn: &[]int{2}},
		{ID: 2, Status: TaskOpen, DependsOn: &[]int{1}},
	}); err == nil {
		t.Fatal("expected a dependency-cycle error from the projected graph")
	}
	for _, tk := range s2.View() {
		if len(tk.DependsOn) != 0 {
			t.Errorf("task %d got dependencies despite the cycle rejection", tk.ID)
		}
	}
}
