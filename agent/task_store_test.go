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
		{Type: TaskTypeResearch, Description: "Read auth code", Prompt: "Read internal/auth/*.go and summarize the token flow."},
		{Type: TaskTypeResearch, Description: "Write tests", Prompt: "Write unit tests for JWT refresh in auth/refresh_test.go."},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Do B"},
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "First", Prompt: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Second", Prompt: "2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Third", Prompt: "3"}}); err != nil {
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
		{Type: TaskTypeResearch, Description: "Persisted task", Prompt: "Should survive reload"},
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
	added, _ := s2.Append([]TaskInput{{Type: TaskTypeResearch, Description: "New after reload", Prompt: "p"}})
	if added[0].ID != 2 {
		t.Fatalf("ID after reload: got %d want 2", added[0].ID)
	}
}

func TestTaskStore_UpdateOnlyChangesStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Original desc", Prompt: "Original prompt"},
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Test", Prompt: "p"}}); err != nil {
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Original", Prompt: "p"}}); err != nil {
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
	if _, err := s1.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task for session A", Prompt: "A"}}); err != nil {
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
	if _, err := s2.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task for session B", Prompt: "B"}}); err != nil {
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
		{Type: TaskTypeResearch, Description: "Build the project", Prompt: "Run make and fix any errors"},
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

	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task", Prompt: "Do stuff"}}); err != nil {
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
			"tasks": [{"type": "research", "description": "Build project", "prompt": "Run make"}]
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
				{"type": "research", "description": "Read auth code", "prompt": "Read the auth module"},
				{"type": "research", "description": "Write tests", "prompt": "Write tests for auth"}
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
		{Type: TaskTypeResearch, Description: "First task", Prompt: "Do first"},
	})
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	firstID := added[0].ID

	// Append a task that depends on the first.
	added2, err := s.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Second task", Prompt: "Do second", DependsOn: []int{firstID}},
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
		{Type: TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: TaskTypeResearch, Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
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
		{Type: TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: TaskTypeResearch, Description: "Second", Prompt: "Do second"},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency 99, got nil")
	}
}

func TestTaskStore_UpdateRejectsNonexistentDependency(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	if _, err := s.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
		{Type: TaskTypeResearch, Description: "Task C", Prompt: "Do C", DependsOn: []int{2}},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A", DependsOn: []int{1}},
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
		{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A", DependsOn: []int{2}},
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatalf("expected error for intra-batch cycle, got nil")
	}
}

func TestTaskStore_AppendRestoresNextIDOnFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	// First append succeeds — nextID becomes 2.
	if _, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	// Failing append should not advance nextID.
	_, err := s.Append([]TaskInput{
		{Type: TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency, got nil")
	}

	// Next successful append should get ID 2, not 3.
	added, err := s.Append([]TaskInput{{Type: TaskTypeResearch, Description: "Task C", Prompt: "Do C"}})
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
		{Type: TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "B", Prompt: "b", DependsOn: []int{1}},
		{Type: TaskTypeResearch, Description: "C", Prompt: "c", DependsOn: []int{1}},
		{Type: TaskTypeResearch, Description: "D", Prompt: "d", DependsOn: []int{2, 3}},
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
		{Type: TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "B", Prompt: "b"},
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
		{Type: TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "B", Prompt: "b", DependsOn: []int{1}},
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
		{Type: TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: TaskTypeResearch, Description: "B", Prompt: "b"},
		{Type: TaskTypeResearch, Description: "C", Prompt: "c"},
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
		{Type: TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: TaskTypeResearch, Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
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

// Task 8: Tool handler tests

func TestTaskListTool_AppendWithDependsOn(t *testing.T) {
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

	// Append a base task and a dependent task via the tool.
	appendRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B", "depends_on": [1]}
			]
		}`),
	})
	if appendRes.IsError {
		t.Fatalf("append error: %s", appendRes.Output)
	}

	// View should show depends_on for task 2.
	viewRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "view"}`),
	})
	if viewRes.IsError {
		t.Fatalf("view error: %s", viewRes.Output)
	}
	if !strings.Contains(viewRes.Output, "1") {
		t.Fatalf("view output missing depends_on: %s", viewRes.Output)
	}

	// Verify store state directly.
	store := sess.getOrCreateTaskStore()
	all := store.View()
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(all))
	}
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("task 2 DependsOn: got %v", all[1].DependsOn)
	}
}

func TestTaskListTool_UpdateShowsNextTask(t *testing.T) {
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

	// Create A, B→A, C→A. Use type=research to avoid auto-verify task creation.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B", "depends_on": [1]},
				{"type": "research", "description": "Task C", "prompt": "Do C", "depends_on": [1]}
			]
		}`),
	})

	// Mark A done — response should mention B and C as ready.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}

	// Response should include progress and mention ready tasks.
	if !strings.Contains(updateRes.Output, "Progress") {
		t.Fatalf("response missing Progress: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "Task B") {
		t.Fatalf("response missing Task B: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "Task C") {
		t.Fatalf("response missing Task C: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "in_progress") {
		t.Fatalf("response missing in_progress suggestion: %s", updateRes.Output)
	}
}

func TestTaskListTool_UpdateShowsAllComplete(t *testing.T) {
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

	// Single task. Use type=research to avoid auto-verify task creation.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"type": "research", "description": "Only task", "prompt": "Do it"}]
		}`),
	})

	// Mark it done.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "All tasks complete") {
		t.Fatalf("response should say 'All tasks complete': %s", updateRes.Output)
	}
}

func TestTaskListTool_UpdateShowsBlocked(t *testing.T) {
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

	// A, B→A, C→B (chain A←B←C). Use type=research to avoid auto-verify task creation.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B", "depends_on": [1]},
				{"type": "research", "description": "Task C", "prompt": "Do C", "depends_on": [2]}
			]
		}`),
	})

	// Mark A in_progress (not done), cancel C — B still blocked on A in_progress.
	// With A in_progress (not eligible) and C cancelled, B is the only open task
	// but A is in_progress so B's dep is not satisfied. No tasks should be ready.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress"}]}`),
	})

	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c3",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 3, "status": "cancelled"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "No tasks are currently ready") {
		t.Fatalf("response should say no tasks ready: %s", updateRes.Output)
	}
}

func TestTaskStore_AutoVerifyUsesReviewerAgentType(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	added, err := s.Append([]TaskInput{
		{Type: TaskTypeImplement, Description: "Build widget", Prompt: "Create widget.go"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added task, got %d", len(added))
	}

	all := s.View()
	// Should have 2 tasks: the implement task + auto-generated verify task.
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks (implement + verify), got %d", len(all))
	}

	verify := all[1]
	if verify.Type != TaskTypeVerify {
		t.Errorf("auto-created task type = %q, want %q", verify.Type, TaskTypeVerify)
	}
	if !strings.Contains(verify.Prompt, `agent_type="reviewer"`) {
		t.Errorf("verify prompt should instruct coordinator to use agent_type=\"reviewer\", got: %s", verify.Prompt)
	}
	if len(verify.DependsOn) != 1 || verify.DependsOn[0] != added[0].ID {
		t.Errorf("verify task should depend on implement task, got depends_on=%v", verify.DependsOn)
	}
}

func TestTaskStore_AutoVerifyAfterFixTask(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	added, err := s.Append([]TaskInput{
		{Type: TaskTypeFix, Description: "Fix edge case", Prompt: "Handle negative input"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added task, got %d", len(added))
	}

	all := s.View()
	// Fix tasks should also get auto-verify.
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks (fix + verify), got %d", len(all))
	}

	verify := all[1]
	if verify.Type != TaskTypeVerify {
		t.Errorf("auto-created task type = %q, want %q", verify.Type, TaskTypeVerify)
	}
	if len(verify.DependsOn) != 1 || verify.DependsOn[0] != added[0].ID {
		t.Errorf("verify task should depend on fix task, got depends_on=%v", verify.DependsOn)
	}
}

func TestTaskStore_AutoVerifyDisabled(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")
	s.AutoVerify = false

	added, err := s.Append([]TaskInput{
		{Type: TaskTypeImplement, Description: "Build widget", Prompt: "Create widget.go"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added task, got %d", len(added))
	}

	all := s.View()
	// No auto-generated verify task when AutoVerify is false.
	if len(all) != 1 {
		t.Fatalf("expected 1 task (auto-verify disabled), got %d", len(all))
	}
	if all[0].Type != TaskTypeImplement {
		t.Errorf("task type = %q, want %q", all[0].Type, TaskTypeImplement)
	}
}

func TestTaskStore_AutoVerifyDisabledForFixTasks(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")
	s.AutoVerify = false

	added, err := s.Append([]TaskInput{
		{Type: TaskTypeFix, Description: "Fix edge case", Prompt: "Handle negative input"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added task, got %d", len(added))
	}

	all := s.View()
	if len(all) != 1 {
		t.Fatalf("expected 1 task (auto-verify disabled), got %d", len(all))
	}
}

func TestTaskStore_AutoVerifyDefaultEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")
	// AutoVerify defaults to true — verify tasks should still be created.

	s.Append([]TaskInput{
		{Type: TaskTypeImplement, Description: "Build it", Prompt: "Do the thing"},
	})

	all := s.View()
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks (implement + verify with default AutoVerify=true), got %d", len(all))
	}
}

func TestTaskListTool_DisableAutoVerifyViaSessionConfig(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{
		DisableAutoVerify: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append an implement task — should NOT generate auto-verify.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"type": "implement", "description": "Build widget", "prompt": "Create widget.go"}]
		}`),
	})

	store := sess.getOrCreateTaskStore()
	all := store.View()
	if len(all) != 1 {
		t.Fatalf("expected 1 task (auto-verify disabled via config), got %d", len(all))
	}
	if all[0].Type != TaskTypeImplement {
		t.Errorf("task type = %q, want %q", all[0].Type, TaskTypeImplement)
	}
}

func TestTaskStore_AutoVerifyPromptFocusesOnRejection(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir, "test-session")

	s.Append([]TaskInput{
		{Type: TaskTypeImplement, Description: "Build it", Prompt: "Do the thing"},
	})

	all := s.View()
	verify := all[1]
	// Should focus on rejection-worthy issues including leftover artifacts.
	if !strings.Contains(verify.Prompt, "reject") {
		t.Errorf("verify prompt should mention rejection, got: %s", verify.Prompt)
	}
	if !strings.Contains(verify.Prompt, "leftover build artifacts") {
		t.Errorf("verify prompt should mention leftover build artifacts, got: %s", verify.Prompt)
	}
	if !strings.Contains(verify.Prompt, "rewarded for finding legitimate problems") {
		t.Errorf("verify prompt should incentivize finding real problems, got: %s", verify.Prompt)
	}
}

func TestTaskListTool_AppendShowsProgressAndNextTask(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{
		DisableAutoVerify: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append tasks with dependencies: A (no deps), B→A.
	appendRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B", "depends_on": [1]}
			]
		}`),
	})
	if appendRes.IsError {
		t.Fatalf("append error: %s", appendRes.Output)
	}

	// Append response should include progress info.
	if !strings.Contains(appendRes.Output, "Progress") {
		t.Fatalf("append response should include Progress summary: %s", appendRes.Output)
	}
	// Should show Task A as the next eligible task.
	if !strings.Contains(appendRes.Output, "Task A") {
		t.Fatalf("append response should mention next eligible task: %s", appendRes.Output)
	}
	// Should suggest marking in_progress.
	if !strings.Contains(appendRes.Output, "in_progress") {
		t.Fatalf("append response should suggest marking in_progress: %s", appendRes.Output)
	}
}

func TestTaskListTool_AppendShowsAllReady(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), NewLocalExecutionEnvironment(dir), SessionConfig{
		DisableAutoVerify: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append two independent tasks (both eligible immediately).
	appendRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task X", "prompt": "Do X"},
				{"type": "research", "description": "Task Y", "prompt": "Do Y"}
			]
		}`),
	})
	if appendRes.IsError {
		t.Fatalf("append error: %s", appendRes.Output)
	}

	// Both tasks should appear as ready.
	if !strings.Contains(appendRes.Output, "Task X") {
		t.Fatalf("append response should mention Task X: %s", appendRes.Output)
	}
	if !strings.Contains(appendRes.Output, "Task Y") {
		t.Fatalf("append response should mention Task Y: %s", appendRes.Output)
	}
}
