package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prime-radiant/serf/internal/llm"
)

func TestTaskStore_AppendAndView(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir)

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
	if added[0].Status != TaskUndone || added[1].Status != TaskUndone {
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
	s := NewTaskStore(dir)

	s.Append([]TaskInput{
		{Description: "Task A", Prompt: "Do A"},
		{Description: "Task B", Prompt: "Do B"},
	})

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
	s := NewTaskStore(dir)

	s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}})

	err := s.Update([]TaskUpdate{{ID: 99, Status: TaskDone}})
	if err == nil {
		t.Fatalf("expected error for unknown ID")
	}
}

func TestTaskStore_UpdateRejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir)

	s.Append([]TaskInput{{Description: "Task A", Prompt: "Do A"}})

	err := s.Update([]TaskUpdate{{ID: 1, Status: TaskStatus("deleted")}})
	if err == nil {
		t.Fatalf("expected error for invalid status")
	}
}

func TestTaskStore_IDsAreMonotonic(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir)

	s.Append([]TaskInput{{Description: "First", Prompt: "1"}})
	s.Append([]TaskInput{{Description: "Second", Prompt: "2"}})
	s.Append([]TaskInput{{Description: "Third", Prompt: "3"}})

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
	s1 := NewTaskStore(dir)
	s1.Append([]TaskInput{
		{Description: "Persisted task", Prompt: "Should survive reload"},
	})
	s1.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})

	// Load fresh store from same directory.
	s2 := NewTaskStore(dir)
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
	s := NewTaskStore(dir)

	s.Append([]TaskInput{
		{Description: "Original desc", Prompt: "Original prompt"},
	})

	s.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})

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
	s := NewTaskStore(dir)

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
	s := NewTaskStore(dir)

	s.Append([]TaskInput{{Description: "Test", Prompt: "p"}})

	path := filepath.Join(dir, ".serf", "tasks.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected tasks.json to exist at %s", path)
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
	if !strings.Contains(viewRes.Output, "undone") {
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
