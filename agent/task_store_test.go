package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestTaskStore_AppendAndView(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	added, err := s.Append([]TaskInput{
		{Description: "Read auth code", Prompt: "Read internal/auth/*.go and summarize the token flow."},
		{Description: "Write tests", Prompt: "Write unit tests for JWT refresh in auth/refresh_test.go."},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("expected 2 added tasks, got %d", len(added))
	}
	if added[0].ID != 1 || added[1].ID != 2 {
		t.Fatalf("IDs: got %d, %d", added[0].ID, added[1].ID)
	}
	if added[0].Status != TaskOpen || added[1].Status != TaskOpen {
		t.Fatalf("statuses: got %q, %q", added[0].Status, added[1].Status)
	}
	if added[0].Description != "Read auth code" {
		t.Fatalf("description: got %q", added[0].Description)
	}
	if added[1].Prompt != "Write unit tests for JWT refresh in auth/refresh_test.go." {
		t.Fatalf("prompt: got %q", added[1].Prompt)
	}

	all := s.View()
	if len(all) != 2 {
		t.Fatalf("View: expected 2 tasks, got %d", len(all))
	}
}

func TestTaskStore_UpdateStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A"},
		{Description: "Task B", Prompt: "Do B"},
	}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]TaskUpdate{
		{ID: 1, Status: TaskDone},
		{ID: 2, Status: TaskCancelled},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	all := s.View()
	if all[0].Status != TaskDone {
		t.Fatalf("task 1 status: got %q want %q", all[0].Status, TaskDone)
	}
	if all[1].Status != TaskCancelled {
		t.Fatalf("task 2 status: got %q want %q", all[1].Status, TaskCancelled)
	}
}

func TestTaskStore_UpdateRejectsUnknownID(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]TaskUpdate{{ID: 99, Status: TaskDone}})
	if err == nil {
		t.Fatalf("expected error for unknown ID")
	}
}

func TestTaskStore_UpdateInProgress(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}})
	if err != nil {
		t.Fatalf("Update to in_progress: %v", err)
	}

	all := s.View()
	if all[0].Status != TaskInProgress {
		t.Fatalf("task 1 status: got %q want %q", all[0].Status, TaskInProgress)
	}
}

func TestTaskStore_UpdateRejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskStatus("deleted")}})
	if err == nil {
		t.Fatalf("expected error for invalid status")
	}
}

func TestTaskStore_IDsAreMonotonic(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "First", Prompt: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]TaskInput{{Description: "Second", Prompt: "2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]TaskInput{{Description: "Third", Prompt: "3"}}); err != nil {
		t.Fatal(err)
	}

	all := s.View()
	for i := 0; i < len(all)-1; i++ {
		if all[i+1].ID <= all[i].ID {
			t.Fatalf("IDs not monotonic: %d followed by %d", all[i].ID, all[i+1].ID)
		}
	}
}

func TestTaskStore_PersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()

	// Create and populate store.
	s1 := NewTaskStore(dir, "test-session")
	if _, err := s1.Append([]TaskInput{
		{Description: "Persisted task", Prompt: "Should survive reload"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Update([]TaskUpdate{{ID: 1, Status: TaskDone}}); err != nil {
		t.Fatal(err)
	}

	// Load fresh store from same directory.
	s2 := NewTaskStore(dir, "test-session")
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	all := s2.View()
	if len(all) != 1 {
		t.Fatalf("expected 1 task after reload, got %d", len(all))
	}
	if all[0].Description != "Persisted task" {
		t.Fatalf("description after reload: %q", all[0].Description)
	}
	if all[0].Status != TaskDone {
		t.Fatalf("status after reload: %q", all[0].Status)
	}

	// New appends should continue ID sequence.
	added, _ := s2.Append([]TaskInput{{Description: "New after reload", Prompt: "p"}})
	if added[0].ID != 2 {
		t.Fatalf("ID after reload: got %d want 2", added[0].ID)
	}
}

func TestTaskStore_UpdateOnlyChangesStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "Original desc", Prompt: "Original prompt"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}}); err != nil {
		t.Fatal(err)
	}

	all := s.View()
	if all[0].Description != "Original desc" {
		t.Fatalf("description changed: got %q", all[0].Description)
	}
	if all[0].Prompt != "Original prompt" {
		t.Fatalf("prompt changed: got %q", all[0].Prompt)
	}
}

func TestTaskStore_LoadNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Load should succeed with empty store when no file exists.
	if err := s.Load(); err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	if len(s.View()) != 0 {
		t.Fatalf("expected empty store, got %d tasks", len(s.View()))
	}
}

func TestTaskStore_FileExistsOnDisk(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Test", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "tasks", "test-session.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected tasks.json to exist at %s", path)
	}
}

func TestTaskStore_ViewReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Original", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}

	// Mutate the returned slice.
	view := s.View()
	view[0].Description = "Mutated"
	view[0].Status = TaskDone

	// Store should be unchanged.
	fresh := s.View()
	if fresh[0].Description != "Original" {
		t.Fatalf("View did not return a defensive copy: description is %q", fresh[0].Description)
	}
	if fresh[0].Status != TaskOpen {
		t.Fatalf("View did not return a defensive copy: status is %q", fresh[0].Status)
	}
}

func TestTaskStore_ScopedBySessionID(t *testing.T) {
	dir := t.TempDir()

	s1 := NewTaskStore(dir, "session-aaa")
	s2 := NewTaskStore(dir, "session-bbb")

	// Add a task in session 1.
	if _, err := s1.Append([]TaskInput{{Description: "Task for session A", Prompt: "A"}}); err != nil {
		t.Fatal(err)
	}

	// Session 2 should not see it.
	if err := s2.Load(); err != nil {
		t.Fatalf("s2.Load: %v", err)
	}
	if len(s2.View()) != 0 {
		t.Fatalf("session-bbb should have 0 tasks, got %d", len(s2.View()))
	}

	// Add a task in session 2.
	if _, err := s2.Append([]TaskInput{{Description: "Task for session B", Prompt: "B"}}); err != nil {
		t.Fatal(err)
	}

	// Reload session 1 — should still have only its task.
	s1r := NewTaskStore(dir, "session-aaa")
	if err := s1r.Load(); err != nil {
		t.Fatalf("s1r.Load: %v", err)
	}
	if len(s1r.View()) != 1 || s1r.View()[0].Description != "Task for session A" {
		t.Fatalf("session-aaa should have 1 task, got %v", s1r.View())
	}
}

func TestTaskStore_UpdateWithNotes(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "Build the project", Prompt: "Run make and fix any errors"},
	}); err != nil {
		t.Fatal(err)
	}

	// First note.
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress, Notes: "Tried make -j4, got linker error: undefined reference to libfoo"}})
	if err != nil {
		t.Fatalf("Update with notes: %v", err)
	}
	all := s.View()
	if len(all[0].Notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(all[0].Notes))
	}
	if all[0].Notes[0] != "Tried make -j4, got linker error: undefined reference to libfoo" {
		t.Fatalf("note content: %q", all[0].Notes[0])
	}

	// Second note appends.
	err = s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress, Notes: "Installed libfoo-dev, retrying"}})
	if err != nil {
		t.Fatalf("Update second note: %v", err)
	}
	all = s.View()
	if len(all[0].Notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(all[0].Notes))
	}

	// Empty notes field should not append.
	err = s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
	if err != nil {
		t.Fatalf("Update without notes: %v", err)
	}
	all = s.View()
	if len(all[0].Notes) != 2 {
		t.Fatalf("expected 2 notes after status-only update, got %d", len(all[0].Notes))
	}
}

func TestTaskStore_NotesPersistAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{{Description: "Task", Prompt: "Do stuff"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress, Notes: "First approach failed"}}); err != nil {
		t.Fatal(err)
	}

	// Reload from disk.
	s2 := NewTaskStore(dir, "test-session")
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := s2.View()
	if len(all[0].Notes) != 1 || all[0].Notes[0] != "First approach failed" {
		t.Fatalf("notes after reload: %v", all[0].Notes)
	}
}

func TestTaskListTool_UpdateWithNotes(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append a task.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"description": "Build project", "prompt": "Run make"}]
		}`),
	})

	// Update with notes.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c2",
		Name: "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress", "notes": "make failed with missing libfoo"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update with notes error: %s", updateRes.Output)
	}

	// View should show notes.
	viewRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c3",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "view"}`),
	})
	if !strings.Contains(viewRes.Output, "make failed with missing libfoo") {
		t.Fatalf("view should contain notes: %s", viewRes.Output)
	}
}

func TestTaskListTool_AppendViewUpdate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append tasks.
	appendRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"description": "Read auth code", "prompt": "Read the auth module"},
				{"description": "Write tests", "prompt": "Write tests for auth"}
			]
		}`),
	})
	if appendRes.IsError {
		t.Fatalf("append error: %s", appendRes.Output)
	}
	if !strings.Contains(appendRes.Output, "Read auth code") {
		t.Fatalf("append output missing description: %s", appendRes.Output)
	}

	// View tasks.
	viewRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "view"}`),
	})
	if viewRes.IsError {
		t.Fatalf("view error: %s", viewRes.Output)
	}
	if !strings.Contains(viewRes.Output, "open") {
		t.Fatalf("view output missing status: %s", viewRes.Output)
	}

	// Update task status.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c3",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}

	// Verify via view.
	viewRes2 := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c4",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "view"}`),
	})
	if !strings.Contains(viewRes2.Output, `"done"`) {
		t.Fatalf("view after update missing done status: %s", viewRes2.Output)
	}
}

func TestTaskStore_AppendWithDependsOn(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Append a prerequisite task first.
	added, err := s.Append([]TaskInput{
		{Description: "First task", Prompt: "Do first"},
	})
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	firstID := added[0].ID

	// Append a task that depends on the first.
	added2, err := s.Append([]TaskInput{
		{Description: "Second task", Prompt: "Do second", DependsOn: []int{firstID}},
	})
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if len(added2[0].DependsOn) != 1 || added2[0].DependsOn[0] != firstID {
		t.Fatalf("DependsOn not set: got %v", added2[0].DependsOn)
	}

	// View should include the DependsOn.
	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != firstID {
		t.Fatalf("View DependsOn: got %v", all[1].DependsOn)
	}
}

func TestTaskStore_DependsOnPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "First", Prompt: "Do first"},
		{Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Reload from disk.
	s2 := NewTaskStore(dir, "test-session")
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	all := s2.View()
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks after reload, got %d", len(all))
	}
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn after reload: got %v", all[1].DependsOn)
	}
	// Task without deps should have nil/empty DependsOn.
	if len(all[0].DependsOn) != 0 {
		t.Fatalf("task 1 should have no DependsOn, got %v", all[0].DependsOn)
	}
}

func TestTaskStore_UpdateDependsOn(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "First", Prompt: "Do first"},
		{Description: "Second", Prompt: "Do second"},
	}); err != nil {
		t.Fatal(err)
	}

	// Set depends_on via Update.
	deps := []int{1}
	if err := s.Update([]TaskUpdate{{ID: 2, Status: TaskOpen, DependsOn: &deps}}); err != nil {
		t.Fatalf("Update with DependsOn: %v", err)
	}
	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn after Update: got %v", all[1].DependsOn)
	}

	// Clear depends_on with empty slice.
	empty := []int{}
	if err := s.Update([]TaskUpdate{{ID: 2, Status: TaskOpen, DependsOn: &empty}}); err != nil {
		t.Fatalf("Update clear DependsOn: %v", err)
	}
	all = s.View()
	if len(all[1].DependsOn) != 0 {
		t.Fatalf("DependsOn should be cleared: got %v", all[1].DependsOn)
	}
}

// Task 4: Dependency validation tests

func TestTaskStore_AppendRejectsNonexistentDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	_, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency 99, got nil")
	}
}

func TestTaskStore_UpdateRejectsNonexistentDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A"},
	}); err != nil {
		t.Fatal(err)
	}

	deps := []int{99}
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency 99, got nil")
	}
}

func TestTaskStore_RejectsCyclicDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Append A and B with no deps first.
	if _, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A"},
		{Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Now try to make A depend on B — creates cycle A→B→A.
	deps := []int{2}
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for cyclic dependency A→B→A, got nil")
	}
}

func TestTaskStore_RejectsTransitiveCycle(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// A, B→A, C→B  (chain A←B←C)
	if _, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A"},
		{Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
		{Description: "Task C", Prompt: "Do C", DependsOn: []int{2}},
	}); err != nil {
		t.Fatal(err)
	}

	// Make A depend on C → closes the cycle A←B←C←A.
	deps := []int{3}
	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for transitive cycle A→C→B→A, got nil")
	}
}

func TestTaskStore_RejectsSelfDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	_, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatalf("expected error for self-dependency (ID 1 depends on 1), got nil")
	}
}

func TestTaskStore_RejectsIntraBatchCycle(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// Both tasks reference each other within the same Append call.
	// Task at nextID=1 depends on 2, task at nextID=2 depends on 1.
	_, err := s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A", DependsOn: []int{2}},
		{Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatalf("expected error for intra-batch cycle, got nil")
	}
}

func TestTaskStore_AppendRestoresNextIDOnFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// First append succeeds — nextID becomes 2.
	if _, err := s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	// Failing append should not advance nextID.
	_, err := s.Append([]TaskInput{
		{Description: "Task B", Prompt: "Do B", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency, got nil")
	}

	// Next successful append should get ID 2, not 3.
	added, err := s.Append([]TaskInput{{Description: "Task C", Prompt: "Do C"}})
	if err != nil {
		t.Fatalf("Append after failed: %v", err)
	}
	if added[0].ID != 2 {
		t.Fatalf("expected ID 2 after failed append, got %d", added[0].ID)
	}
}

// ids extracts task IDs from a slice of tasks.
func ids(tasks []Task) []int {
	out := make([]int, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

// Task 5: NextEligible tests

func TestTaskStore_NextEligible(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// A (no deps), B→A, C→A, D→[B,C]
	if _, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
		{Description: "C", Prompt: "c", DependsOn: []int{1}},
		{Description: "D", Prompt: "d", DependsOn: []int{2, 3}},
	}); err != nil {
		t.Fatal(err)
	}

	// Initially only A is eligible (no deps).
	eligible := ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 1 {
		t.Fatalf("step 0: expected [1], got %v", eligible)
	}

	// Mark A done — B and C become eligible.
	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 2 || eligible[0] != 2 || eligible[1] != 3 {
		t.Fatalf("step 1: expected [2 3], got %v", eligible)
	}

	// Mark B done — C still eligible, D not yet (C still open).
	if err := s.Update([]TaskUpdate{{ID: 2, Status: TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 3 {
		t.Fatalf("step 2: expected [3], got %v", eligible)
	}

	// Mark C done — D becomes eligible.
	if err := s.Update([]TaskUpdate{{ID: 3, Status: TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 4 {
		t.Fatalf("step 3: expected [4], got %v", eligible)
	}
}

func TestTaskStore_NextEligibleSkipsInProgress(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
	}); err != nil {
		t.Fatal(err)
	}

	// Mark A in_progress — should not appear in eligible list.
	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskInProgress}}); err != nil {
		t.Fatal(err)
	}

	eligible := ids(s.NextEligible())
	for _, id := range eligible {
		if id == 1 {
			t.Fatalf("in_progress task 1 should not appear in NextEligible")
		}
	}
	// B should still be eligible (no deps, open).
	if len(eligible) != 1 || eligible[0] != 2 {
		t.Fatalf("expected [2], got %v", eligible)
	}
}

func TestTaskStore_NextEligibleCancelledSatisfiesDeps(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// B depends on A; A gets cancelled.
	if _, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]TaskUpdate{{ID: 1, Status: TaskCancelled}}); err != nil {
		t.Fatal(err)
	}

	// B should become eligible since its dep (A) is cancelled.
	eligible := ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 2 {
		t.Fatalf("expected [2] after A cancelled, got %v", eligible)
	}
}

// Task 6: Progress summary tests

func TestTaskStore_Progress(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "A", Prompt: "a"},
		{Description: "B", Prompt: "b"},
		{Description: "C", Prompt: "c"},
	}); err != nil {
		t.Fatal(err)
	}

	// No tasks done yet.
	total, done := s.Progress()
	if total != 3 || done != 0 {
		t.Fatalf("initial: expected total=3 done=0, got total=%d done=%d", total, done)
	}

	// Mark one done, one cancelled.
	if err := s.Update([]TaskUpdate{
		{ID: 1, Status: TaskDone},
		{ID: 2, Status: TaskCancelled},
	}); err != nil {
		t.Fatal(err)
	}

	total, done = s.Progress()
	if total != 3 || done != 1 {
		t.Fatalf("after updates: expected total=3 done=1, got total=%d done=%d", total, done)
	}
}

func TestTaskStore_UpdateOmittedDependsOnPreserves(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Description: "First", Prompt: "Do first"},
		{Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Update status without touching DependsOn (nil pointer = no change).
	if err := s.Update([]TaskUpdate{{ID: 2, Status: TaskInProgress}}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn should be preserved: got %v", all[1].DependsOn)
	}
}
