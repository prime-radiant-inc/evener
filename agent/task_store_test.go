package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func TestTaskStore_TimestampsMintedAutomatically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Date(2026, 6, 25, 14, 0, 0, 0, time.UTC)
	clock := base
	s := taskpkg.NewTaskStore(dir, "ts-session").SetClock(func() time.Time { return clock })

	// Append stamps created_at == updated_at and leaves completed_at unset.
	added, err := s.Append([]taskpkg.TaskInput{{Description: "A", Prompt: "do a"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	a := added[0]
	if a.CreatedAt == nil || !a.CreatedAt.Equal(base) {
		t.Fatalf("created_at not minted on append: %v", a.CreatedAt)
	}
	if a.UpdatedAt == nil || !a.UpdatedAt.Equal(base) {
		t.Fatalf("updated_at not minted on append: %v", a.UpdatedAt)
	}
	if a.CompletedAt != nil {
		t.Fatalf("open task must not carry completed_at: %v", a.CompletedAt)
	}

	// An update advances updated_at; reaching done stamps completed_at while
	// created_at stays put.
	clock = base.Add(5 * time.Minute)
	if err := s.Update([]taskpkg.TaskUpdate{{ID: a.ID, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatalf("Update done: %v", err)
	}
	got := s.View()[0]
	if got.CreatedAt == nil || !got.CreatedAt.Equal(base) {
		t.Fatalf("created_at must not change on update: %v", got.CreatedAt)
	}
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(clock) {
		t.Fatalf("updated_at not advanced on update: %v", got.UpdatedAt)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(clock) {
		t.Fatalf("completed_at not stamped on done: %v", got.CompletedAt)
	}

	// Reopening a done task clears completed_at and advances updated_at.
	clock = base.Add(10 * time.Minute)
	if err := s.Update([]taskpkg.TaskUpdate{{ID: a.ID, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("Update reopen: %v", err)
	}
	got = s.View()[0]
	if got.CompletedAt != nil {
		t.Fatalf("reopened task must clear completed_at: %v", got.CompletedAt)
	}
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(clock) {
		t.Fatalf("updated_at not advanced on reopen: %v", got.UpdatedAt)
	}

	// Timestamps survive a round-trip through disk.
	s2 := taskpkg.NewTaskStore(dir, "ts-session")
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := s2.View()[0]
	if r.CreatedAt == nil || !r.CreatedAt.Equal(base) {
		t.Fatalf("created_at lost on reload: %v", r.CreatedAt)
	}
}

func TestTaskStore_AppendAndView(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	added, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Read auth code", Prompt: "Read internal/auth/*.go and summarize the token flow."},
		{Type: taskpkg.TaskTypeResearch, Description: "Write tests", Prompt: "Write unit tests for JWT refresh in auth/refresh_test.go."},
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
	if added[0].Status != taskpkg.TaskOpen || added[1].Status != taskpkg.TaskOpen {
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B"},
	}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]taskpkg.TaskUpdate{
		{ID: 1, Status: taskpkg.TaskDone},
		{ID: 2, Status: taskpkg.TaskCancelled},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	all := s.View()
	if all[0].Status != taskpkg.TaskDone {
		t.Fatalf("task 1 status: got %q want %q", all[0].Status, taskpkg.TaskDone)
	}
	if all[1].Status != taskpkg.TaskCancelled {
		t.Fatalf("task 2 status: got %q want %q", all[1].Status, taskpkg.TaskCancelled)
	}
}

func TestTaskStore_UpdateRejectsUnknownID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]taskpkg.TaskUpdate{{ID: 99, Status: taskpkg.TaskDone}})
	if err == nil {
		t.Fatalf("expected error for unknown ID")
	}
}

func TestTaskStore_UpdateInProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}})
	if err != nil {
		t.Fatalf("Update to in_progress: %v", err)
	}

	all := s.View()
	if all[0].Status != taskpkg.TaskInProgress {
		t.Fatalf("task 1 status: got %q want %q", all[0].Status, taskpkg.TaskInProgress)
	}
}

func TestTaskStore_UpdateRejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskStatus("deleted")}})
	if err == nil {
		t.Fatalf("expected error for invalid status")
	}
}

func TestTaskStore_UpdateRejectsMultipleInProgressInBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B"},
	}); err != nil {
		t.Fatal(err)
	}

	err := s.Update([]taskpkg.TaskUpdate{
		{ID: 1, Status: taskpkg.TaskInProgress},
		{ID: 2, Status: taskpkg.TaskInProgress},
	})
	if err == nil {
		t.Fatalf("expected error for setting two tasks to in_progress in one update")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Fatalf("error should mention in_progress: %v", err)
	}

	// Neither task should have been moved to in_progress (whole batch rejected).
	all := s.View()
	for _, task := range all {
		if task.Status == taskpkg.TaskInProgress {
			t.Fatalf("no task should be in_progress after rejected batch; got %+v", task)
		}
	}
}

func TestTaskStore_UpdateRejectsInProgressWhenOneAlreadyExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("first in_progress update: %v", err)
	}

	err := s.Update([]taskpkg.TaskUpdate{{ID: 2, Status: taskpkg.TaskInProgress}})
	if err == nil {
		t.Fatalf("expected error when setting a second task to in_progress while another is in_progress")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Fatalf("error should mention in_progress: %v", err)
	}

	// Existing in_progress task should still be in_progress; new one should not.
	all := s.View()
	if all[0].Status != taskpkg.TaskInProgress {
		t.Fatalf("task 1 should remain in_progress; got %q", all[0].Status)
	}
	if all[1].Status == taskpkg.TaskInProgress {
		t.Fatalf("task 2 should not be in_progress; got %q", all[1].Status)
	}
}

func TestTaskStore_UpdateAllowsMovingInProgressToDoneThenStartingAnother(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("first in_progress: %v", err)
	}

	// Agent completes task 1 and starts task 2 in one batch — allowed.
	if err := s.Update([]taskpkg.TaskUpdate{
		{ID: 1, Status: taskpkg.TaskDone},
		{ID: 2, Status: taskpkg.TaskInProgress},
	}); err != nil {
		t.Fatalf("unexpected error transitioning 1→done and 2→in_progress in one batch: %v", err)
	}

	all := s.View()
	if all[0].Status != taskpkg.TaskDone {
		t.Fatalf("task 1 should be done; got %q", all[0].Status)
	}
	if all[1].Status != taskpkg.TaskInProgress {
		t.Fatalf("task 2 should be in_progress; got %q", all[1].Status)
	}
}

func TestTaskStore_IDsAreMonotonic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "First", Prompt: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Second", Prompt: "2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Third", Prompt: "3"}}); err != nil {
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
	t.Parallel()
	dir := t.TempDir()

	// Create and populate store.
	s1 := taskpkg.NewTaskStore(dir, "test-session")
	if _, err := s1.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Persisted task", Prompt: "Should survive reload"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatal(err)
	}

	// Load fresh store from same directory.
	s2 := taskpkg.NewTaskStore(dir, "test-session")
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
	if all[0].Status != taskpkg.TaskDone {
		t.Fatalf("status after reload: %q", all[0].Status)
	}

	// New appends should continue ID sequence.
	added, _ := s2.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "New after reload", Prompt: "p"}})
	if added[0].ID != 2 {
		t.Fatalf("ID after reload: got %d want 2", added[0].ID)
	}
}

func TestTaskStore_UpdateOnlyChangesStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Original desc", Prompt: "Original prompt"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}}); err != nil {
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// Load should succeed with empty store when no file exists.
	if err := s.Load(); err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	if len(s.View()) != 0 {
		t.Fatalf("expected empty store, got %d tasks", len(s.View()))
	}
}

func TestTaskStore_FileExistsOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Test", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "tasks", "test-session.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected tasks.json to exist at %s", path)
	}
}

func TestTaskStore_ViewReturnsCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Original", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}

	// Mutate the returned slice.
	view := s.View()
	view[0].Description = "Mutated"
	view[0].Status = taskpkg.TaskDone

	// Store should be unchanged.
	fresh := s.View()
	if fresh[0].Description != "Original" {
		t.Fatalf("View did not return a defensive copy: description is %q", fresh[0].Description)
	}
	if fresh[0].Status != taskpkg.TaskOpen {
		t.Fatalf("View did not return a defensive copy: status is %q", fresh[0].Status)
	}
}

func TestTaskStore_ScopedBySessionID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	s1 := taskpkg.NewTaskStore(dir, "session-aaa")
	s2 := taskpkg.NewTaskStore(dir, "session-bbb")

	// Add a task in session 1.
	if _, err := s1.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task for session A", Prompt: "A"}}); err != nil {
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
	if _, err := s2.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task for session B", Prompt: "B"}}); err != nil {
		t.Fatal(err)
	}

	// Reload session 1 — should still have only its task.
	s1r := taskpkg.NewTaskStore(dir, "session-aaa")
	if err := s1r.Load(); err != nil {
		t.Fatalf("s1r.Load: %v", err)
	}
	if len(s1r.View()) != 1 || s1r.View()[0].Description != "Task for session A" {
		t.Fatalf("session-aaa should have 1 task, got %v", s1r.View())
	}
}

func TestTaskStore_UpdateWithNotes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Build the project", Prompt: "Run make and fix any errors"},
	}); err != nil {
		t.Fatal(err)
	}

	// First note.
	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, Notes: "Tried make -j4, got linker error: undefined reference to libfoo"}})
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
	err = s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, Notes: "Installed libfoo-dev, retrying"}})
	if err != nil {
		t.Fatalf("Update second note: %v", err)
	}
	all = s.View()
	if len(all[0].Notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(all[0].Notes))
	}

	// Empty notes field should not append.
	err = s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}})
	if err != nil {
		t.Fatalf("Update without notes: %v", err)
	}
	all = s.View()
	if len(all[0].Notes) != 2 {
		t.Fatalf("expected 2 notes after status-only update, got %d", len(all[0].Notes))
	}
}

func TestTaskListSchema_ReasoningEffortEnumPerProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile *provider.Profile
		want    []string
	}{
		{"openai", NewOpenAIProfile("test"), []string{"low", "medium", "high", "xhigh"}},
		{"anthropic", newAnthropicProfile("test"), []string{"low", "medium", "high", "max"}},
		{"gemini", newGeminiProfile("test"), []string{"low", "medium", "high"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.profile.ReasoningEffortLevels(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReasoningEffortLevels: got %v, want %v", got, tc.want)
			}
			// The enum should surface in the task_list update schema.
			var td *llm.ToolDefinition
			for i := range tc.profile.ToolDefinitions() {
				d := tc.profile.ToolDefinitions()[i]
				if d.Name == "task_list" {
					td = &d
					break
				}
			}
			if td == nil {
				t.Fatal("task_list tool definition not found in profile")
			}
			props := td.Parameters["properties"].(map[string]any)

			tasks := props["tasks"].(map[string]any)
			taskItems := tasks["items"].(map[string]any)
			taskProps := taskItems["properties"].(map[string]any)
			appendEffort := taskProps["reasoning_effort"].(map[string]any)
			appendEnum, ok := appendEffort["enum"].([]string)
			if !ok {
				t.Fatalf("append enum missing from reasoning_effort schema: %v", appendEffort)
			}
			if !reflect.DeepEqual(appendEnum, tc.want) {
				t.Fatalf("append schema enum: got %v, want %v", appendEnum, tc.want)
			}
			gotTypes, ok := taskProps["type"].(map[string]any)["enum"].([]string)
			if !ok {
				t.Fatalf("task type enum missing from append schema: %v", taskProps["type"])
			}
			if !reflect.DeepEqual(gotTypes, []string{"research", "implement", "verify", "fix"}) {
				t.Fatalf("append task type enum: got %v", gotTypes)
			}

			updates := props["updates"].(map[string]any)
			items := updates["items"].(map[string]any)
			updProps := items["properties"].(map[string]any)
			effort := updProps["reasoning_effort"].(map[string]any)
			gotEnum, ok := effort["enum"].([]string)
			if !ok {
				t.Fatalf("enum missing from reasoning_effort schema: %v", effort)
			}
			if !reflect.DeepEqual(gotEnum, tc.want) {
				t.Fatalf("schema enum: got %v, want %v", gotEnum, tc.want)
			}
		})
	}
}

func TestTaskStore_CurrentInProgressReflectsReasoningEffortUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeImplement, Description: "Do the work", Prompt: "Build it", ReasoningEffort: "medium"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatal(err)
	}
	current, ok := s.CurrentInProgress()
	if !ok || current.ReasoningEffort != "medium" {
		t.Fatalf("initial effort: got %q, want medium", current.ReasoningEffort)
	}

	// Escalate the in-progress task.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, ReasoningEffort: "high"}}); err != nil {
		t.Fatal(err)
	}
	current, ok = s.CurrentInProgress()
	if !ok || current.ReasoningEffort != "high" {
		t.Fatalf("after escalate: got %q, want high", current.ReasoningEffort)
	}
}

func TestTaskStore_UpdateReasoningEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeImplement, Description: "Do the work", Prompt: "Build it", ReasoningEffort: "medium"},
	}); err != nil {
		t.Fatal(err)
	}

	// Escalate to high.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, ReasoningEffort: "high"}}); err != nil {
		t.Fatalf("escalate to high: %v", err)
	}
	if got := s.View()[0].ReasoningEffort; got != "high" {
		t.Fatalf("reasoning_effort after escalate: got %q, want high", got)
	}

	// Omitting reasoning_effort leaves it unchanged.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, Notes: "still working"}}); err != nil {
		t.Fatalf("update without reasoning_effort: %v", err)
	}
	if got := s.View()[0].ReasoningEffort; got != "high" {
		t.Fatalf("reasoning_effort after omit: got %q, want high", got)
	}

	// Store does not validate effort strings — values are passed through to
	// the provider, which knows what it accepts. Schema-level enum on the
	// tool definition prevents the LLM from sending unsupported values.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, ReasoningEffort: "xhigh"}}); err != nil {
		t.Fatalf("xhigh should be accepted by store: %v", err)
	}
	if got := s.View()[0].ReasoningEffort; got != "xhigh" {
		t.Fatalf("reasoning_effort after xhigh: got %q, want xhigh", got)
	}
}

func TestTaskStore_NotesPersistAcrossLoads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task", Prompt: "Do stuff"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress, Notes: "First approach failed"}}); err != nil {
		t.Fatal(err)
	}

	// Reload from disk.
	s2 := taskpkg.NewTaskStore(dir, "test-session")
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := s2.View()
	if len(all[0].Notes) != 1 || all[0].Notes[0] != "First approach failed" {
		t.Fatalf("notes after reload: %v", all[0].Notes)
	}
}

func TestTaskStore_PopulateFromTemplates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-populate")
	store.Load()

	templates := []taskpkg.TaskTemplate{
		{Title: "Inventory", Prompt: "List files", ReasoningEffort: "low"},
		{Title: "Do work", Prompt: "Implement", Insert: "parent_tasks", ReasoningEffort: "xhigh"},
		{Title: "Verify", Prompt: "Check it", ReasoningEffort: "low"},
	}

	err := store.PopulateFromTemplates(templates, nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks := store.View()
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Description != "Inventory" || tasks[0].ReasoningEffort != "low" {
		t.Errorf("task 0: desc=%q effort=%q", tasks[0].Description, tasks[0].ReasoningEffort)
	}
	if tasks[1].Description != "Do work" || tasks[1].Insert != "parent_tasks" {
		t.Errorf("task 1: desc=%q insert=%q", tasks[1].Description, tasks[1].Insert)
	}
	// First task should be auto-started.
	if tasks[0].Status != taskpkg.TaskInProgress {
		t.Errorf("task 0 status: %q, want in_progress", tasks[0].Status)
	}
	if tasks[1].Status != taskpkg.TaskOpen {
		t.Errorf("task 1 status: %q, want open", tasks[1].Status)
	}
}

func TestTaskStore_PopulateFromTemplates_WithParentTasks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-parent")
	store.Load()

	templates := []taskpkg.TaskTemplate{
		{Title: "Understand", Prompt: "Read spec"},
		{Title: "Do work", Prompt: "Implement", Insert: "parent_tasks"},
		{Title: "Verify", Prompt: "Check it"},
	}
	parentTasks := []taskpkg.TaskTemplate{
		{Title: "Fix eigenvalue solver", Prompt: "Use scipy LAPACK"},
		{Title: "Benchmark sizes 2-10", Prompt: "Must beat numpy"},
	}

	err := store.PopulateFromTemplates(templates, parentTasks)
	if err != nil {
		t.Fatal(err)
	}

	tasks := store.View()
	// Understand + 2 parent + Verify = 4 tasks (placeholder replaced)
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d: %+v", len(tasks), tasks)
	}
	if tasks[0].Description != "Understand" {
		t.Errorf("task 0: %q", tasks[0].Description)
	}
	if tasks[1].Description != "Fix eigenvalue solver" {
		t.Errorf("task 1: %q", tasks[1].Description)
	}
	if tasks[2].Description != "Benchmark sizes 2-10" {
		t.Errorf("task 2: %q", tasks[2].Description)
	}
	if tasks[3].Description != "Verify" {
		t.Errorf("task 3: %q", tasks[3].Description)
	}
	// Placeholder should NOT be in the list.
	for _, task := range tasks {
		if task.Insert == "parent_tasks" {
			t.Errorf("placeholder task should have been replaced: %+v", task)
		}
	}
}

func TestTaskStore_PopulateFromTemplates_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-idempotent")
	store.Load()

	templates := []taskpkg.TaskTemplate{
		{Title: "Step 1", Prompt: "Do it"},
	}

	store.PopulateFromTemplates(templates, nil)
	store.PopulateFromTemplates(templates, nil) // second call should be no-op

	tasks := store.View()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task (idempotent), got %d", len(tasks))
	}
}

func TestTaskListTool_UpdateWithNotes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
		ID:        "c2",
		Name:      "task_list",
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

func TestTaskListTool_UpdateReasoningEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"type": "implement", "description": "Do the work", "prompt": "Build it"}]
		}`),
	})

	// Escalate via tool call.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress", "reasoning_effort": "high"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update with reasoning_effort error: %s", updateRes.Output)
	}

	viewRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c3",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "view"}`),
	})
	if !strings.Contains(viewRes.Output, "high") {
		t.Fatalf("view should report reasoning_effort high: %s", viewRes.Output)
	}

	// OpenAI profile accepts xhigh via the tool schema enum.
	xhigh := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c4",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress", "reasoning_effort": "xhigh"}]}`),
	})
	if xhigh.IsError {
		t.Fatalf("xhigh should be accepted by OpenAI profile: %s", xhigh.Output)
	}
}

func TestTaskListTool_AppendPreservesReasoningEffortAndType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	appendRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [{"type": "fix", "description": "Repair flaky test", "prompt": "Stabilize the test after the regression", "reasoning_effort": "xhigh"}]
		}`),
	})
	if appendRes.IsError {
		t.Fatalf("append error: %s", appendRes.Output)
	}

	tasks := sess.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Type != taskpkg.TaskTypeFix {
		t.Fatalf("task type: got %q want %q", tasks[0].Type, taskpkg.TaskTypeFix)
	}
	if tasks[0].ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort: got %q want %q", tasks[0].ReasoningEffort, "xhigh")
	}
}

func TestTaskListTool_AppendViewUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	// Append acknowledges the change minimally — it no longer echoes task
	// descriptions back. Current-task details are delivered via steering.
	if !strings.Contains(appendRes.Output, "Added 2 task(s)") {
		t.Fatalf("append output missing acknowledgment: %s", appendRes.Output)
	}
	if !strings.Contains(appendRes.Output, "Progress: 0/2") {
		t.Fatalf("append output missing progress: %s", appendRes.Output)
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
	if !strings.Contains(viewRes2.Output, "[done]") {
		t.Fatalf("view after update missing done status: %s", viewRes2.Output)
	}
}

func TestTaskStore_AppendWithDependsOn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// Append a prerequisite task first.
	added, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "First task", Prompt: "Do first"},
	})
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	firstID := added[0].ID

	// Append a task that depends on the first.
	added2, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Second task", Prompt: "Do second", DependsOn: []int{firstID}},
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: taskpkg.TaskTypeResearch, Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Reload from disk.
	s2 := taskpkg.NewTaskStore(dir, "test-session")
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: taskpkg.TaskTypeResearch, Description: "Second", Prompt: "Do second"},
	}); err != nil {
		t.Fatal(err)
	}

	// Set depends_on via Update.
	deps := []int{1}
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 2, Status: taskpkg.TaskOpen, DependsOn: &deps}}); err != nil {
		t.Fatalf("Update with DependsOn: %v", err)
	}
	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn after Update: got %v", all[1].DependsOn)
	}

	// Clear depends_on with empty slice.
	empty := []int{}
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 2, Status: taskpkg.TaskOpen, DependsOn: &empty}}); err != nil {
		t.Fatalf("Update clear DependsOn: %v", err)
	}
	all = s.View()
	if len(all[1].DependsOn) != 0 {
		t.Fatalf("DependsOn should be cleared: got %v", all[1].DependsOn)
	}
}

// Task 4: Dependency validation tests

func TestTaskStore_AppendRejectsInvalidDependency(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		dependsOn []int
		failMsg   string
	}{
		{"nonexistent dependency", []int{99}, "expected error for nonexistent dependency 99, got nil"},
		{"self dependency", []int{1}, "expected error for self-dependency (ID 1 depends on 1), got nil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := taskpkg.NewTaskStore(dir, "test-session")

			_, err := s.Append([]taskpkg.TaskInput{
				{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A", DependsOn: tc.dependsOn},
			})
			if err == nil {
				t.Fatal(tc.failMsg)
			}
		})
	}
}

func TestTaskStore_UpdateRejectsNonexistentDependency(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
	}); err != nil {
		t.Fatal(err)
	}

	deps := []int{99}
	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency 99, got nil")
	}
}

func TestTaskStore_RejectsCyclicDependency(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// Append A and B with no deps first.
	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Now try to make A depend on B — creates cycle A→B→A.
	deps := []int{2}
	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for cyclic dependency A→B→A, got nil")
	}
}

func TestTaskStore_RejectsTransitiveCycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// A, B→A, C→B  (chain A←B←C)
	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
		{Type: taskpkg.TaskTypeResearch, Description: "Task C", Prompt: "Do C", DependsOn: []int{2}},
	}); err != nil {
		t.Fatal(err)
	}

	// Make A depend on C → closes the cycle A←B←C←A.
	deps := []int{3}
	err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskOpen, DependsOn: &deps}})
	if err == nil {
		t.Fatalf("expected error for transitive cycle A→C→B→A, got nil")
	}
}

func TestTaskStore_RejectsIntraBatchCycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// Both tasks reference each other within the same Append call.
	// Task at nextID=1 depends on 2, task at nextID=2 depends on 1.
	_, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A", DependsOn: []int{2}},
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{1}},
	})
	if err == nil {
		t.Fatalf("expected error for intra-batch cycle, got nil")
	}
}

func TestTaskStore_AppendRestoresNextIDOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// First append succeeds — nextID becomes 2.
	if _, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task A", Prompt: "Do A"}}); err != nil {
		t.Fatal(err)
	}

	// Failing append should not advance nextID.
	_, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Task B", Prompt: "Do B", DependsOn: []int{99}},
	})
	if err == nil {
		t.Fatalf("expected error for nonexistent dependency, got nil")
	}

	// Next successful append should get ID 2, not 3.
	added, err := s.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeResearch, Description: "Task C", Prompt: "Do C"}})
	if err != nil {
		t.Fatalf("Append after failed: %v", err)
	}
	if added[0].ID != 2 {
		t.Fatalf("expected ID 2 after failed append, got %d", added[0].ID)
	}
}

// ids extracts task IDs from a slice of tasks.
func ids(tasks []taskpkg.Task) []int {
	out := make([]int, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

// Task 5: NextEligible tests

func TestTaskStore_NextEligible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// A (no deps), B→A, C→A, D→[B,C]
	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: taskpkg.TaskTypeResearch, Description: "B", Prompt: "b", DependsOn: []int{1}},
		{Type: taskpkg.TaskTypeResearch, Description: "C", Prompt: "c", DependsOn: []int{1}},
		{Type: taskpkg.TaskTypeResearch, Description: "D", Prompt: "d", DependsOn: []int{2, 3}},
	}); err != nil {
		t.Fatal(err)
	}

	// Initially only A is eligible (no deps).
	eligible := ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 1 {
		t.Fatalf("step 0: expected [1], got %v", eligible)
	}

	// Mark A done — B and C become eligible.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 2 || eligible[0] != 2 || eligible[1] != 3 {
		t.Fatalf("step 1: expected [2 3], got %v", eligible)
	}

	// Mark B done — C still eligible, D not yet (C still open).
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 2, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 3 {
		t.Fatalf("step 2: expected [3], got %v", eligible)
	}

	// Mark C done — D becomes eligible.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 3, Status: taskpkg.TaskDone}}); err != nil {
		t.Fatal(err)
	}
	eligible = ids(s.NextEligible())
	if len(eligible) != 1 || eligible[0] != 4 {
		t.Fatalf("step 3: expected [4], got %v", eligible)
	}
}

func TestTaskStore_NextEligibleSkipsInProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: taskpkg.TaskTypeResearch, Description: "B", Prompt: "b"},
	}); err != nil {
		t.Fatal(err)
	}

	// Mark A in_progress — should not appear in eligible list.
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	// B depends on A; A gets cancelled.
	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: taskpkg.TaskTypeResearch, Description: "B", Prompt: "b", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskCancelled}}); err != nil {
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
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "A", Prompt: "a"},
		{Type: taskpkg.TaskTypeResearch, Description: "B", Prompt: "b"},
		{Type: taskpkg.TaskTypeResearch, Description: "C", Prompt: "c"},
	}); err != nil {
		t.Fatal(err)
	}

	// No tasks done yet.
	total, done := s.Progress()
	if total != 3 || done != 0 {
		t.Fatalf("initial: expected total=3 done=0, got total=%d done=%d", total, done)
	}

	// Mark one done, one cancelled.
	if err := s.Update([]taskpkg.TaskUpdate{
		{ID: 1, Status: taskpkg.TaskDone},
		{ID: 2, Status: taskpkg.TaskCancelled},
	}); err != nil {
		t.Fatal(err)
	}

	total, done = s.Progress()
	if total != 3 || done != 1 {
		t.Fatalf("after updates: expected total=3 done=1, got total=%d done=%d", total, done)
	}
}

func TestTaskStore_UpdateOmittedDependsOnPreserves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := taskpkg.NewTaskStore(dir, "test-session")

	if _, err := s.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "First", Prompt: "Do first"},
		{Type: taskpkg.TaskTypeResearch, Description: "Second", Prompt: "Do second", DependsOn: []int{1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Update status without touching DependsOn (nil pointer = no change).
	if err := s.Update([]taskpkg.TaskUpdate{{ID: 2, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	all := s.View()
	if len(all[1].DependsOn) != 1 || all[1].DependsOn[0] != 1 {
		t.Fatalf("DependsOn should be preserved: got %v", all[1].DependsOn)
	}
}

// Task 8: Tool handler tests

func TestTaskListTool_AppendWithDependsOn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestTaskListTool_UpdateAutoAdvanceFiresSteeringNotOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Append A and B with explicit research tasks.
	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
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

	// Drop any steering already queued from the append.
	sess.mu.Lock()
	sess.steeringQueue = nil
	sess.mu.Unlock()

	// Mark A done — auto-advance should start B via steering, not via tool output.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "done"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}

	// Tool response must stay minimal — no task descriptions, no "Started task".
	if strings.Contains(updateRes.Output, "Task B") {
		t.Fatalf("tool response should not leak next task description; got: %s", updateRes.Output)
	}
	if strings.Contains(updateRes.Output, "Started task") {
		t.Fatalf("tool response should not include 'Started task' line; got: %s", updateRes.Output)
	}
	if strings.Contains(updateRes.Output, "Next open task") {
		t.Fatalf("tool response should not include 'Next open task' summary; got: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "Progress") {
		t.Fatalf("tool response should include Progress: %s", updateRes.Output)
	}

	// Steering queue should carry the current-task SYSTEM-REMINDER for task 2.
	sess.mu.Lock()
	queue := make([]string, 0, len(sess.steeringQueue))
	for _, m := range sess.steeringQueue {
		queue = append(queue, m.Text)
	}
	sess.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("expected 1 steering message after auto-advance, got %d: %v", len(queue), queue)
	}
	if !strings.Contains(queue[0], "<SYSTEM-REMINDER>") {
		t.Fatalf("auto-advance steering should be a SYSTEM-REMINDER: %s", queue[0])
	}
	if !strings.Contains(queue[0], `<CURRENT-TASK id="2">`) {
		t.Fatalf("auto-advance steering should target task 2: %s", queue[0])
	}
	if !strings.Contains(queue[0], "Task B") {
		t.Fatalf("auto-advance steering should include task B title: %s", queue[0])
	}
}

func TestTaskListTool_ManualInProgressFiresSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B"}
			]
		}`),
	})

	sess.mu.Lock()
	sess.steeringQueue = nil
	sess.mu.Unlock()

	// Agent manually marks task 2 in_progress.
	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 2, "status": "in_progress"}]}`),
	})
	if updateRes.IsError {
		t.Fatalf("update error: %s", updateRes.Output)
	}

	// Tool response stays minimal.
	if strings.Contains(updateRes.Output, "Task B") {
		t.Fatalf("tool response should not include task description; got: %s", updateRes.Output)
	}

	// Steering should include the current-task reminder for task 2.
	sess.mu.Lock()
	queue := make([]string, 0, len(sess.steeringQueue))
	for _, m := range sess.steeringQueue {
		queue = append(queue, m.Text)
	}
	sess.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("expected 1 steering message after manual in_progress, got %d: %v", len(queue), queue)
	}
	if !strings.Contains(queue[0], "<SYSTEM-REMINDER>") {
		t.Fatalf("manual in_progress steering should be a SYSTEM-REMINDER: %s", queue[0])
	}
	if !strings.Contains(queue[0], `<CURRENT-TASK id="2">`) {
		t.Fatalf("manual in_progress steering should target task 2: %s", queue[0])
	}
}

func TestTaskListTool_UpdateRejectsMultipleInProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:   "c1",
		Name: "task_list",
		Arguments: json.RawMessage(`{
			"action": "append",
			"tasks": [
				{"type": "research", "description": "Task A", "prompt": "Do A"},
				{"type": "research", "description": "Task B", "prompt": "Do B"}
			]
		}`),
	})

	updateRes := sess.reg.ExecuteCall(ctx, env, llm.ToolCallData{
		ID:        "c2",
		Name:      "task_list",
		Arguments: json.RawMessage(`{"action": "update", "updates": [{"id": 1, "status": "in_progress"}, {"id": 2, "status": "in_progress"}]}`),
	})
	if !updateRes.IsError {
		t.Fatalf("expected tool error for two in_progress in one update; got output: %s", updateRes.Output)
	}
	if !strings.Contains(updateRes.Output, "in_progress") {
		t.Fatalf("error should reference in_progress: %s", updateRes.Output)
	}
}

func TestTaskListTool_UpdateShowsAllComplete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// Single research task.
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

	// Steering should be a SYSTEM-REMINDER so models treat it like other task steering.
	sess.mu.Lock()
	queue := make([]string, 0, len(sess.steeringQueue))
	for _, m := range sess.steeringQueue {
		queue = append(queue, m.Text)
	}
	sess.mu.Unlock()
	if len(queue) == 0 {
		t.Fatal("expected steering message after all tasks complete, got none")
	}
	last := queue[len(queue)-1]
	if !strings.Contains(last, "<SYSTEM-REMINDER>") {
		t.Fatalf("all-done steering should be wrapped in <SYSTEM-REMINDER>: %s", last)
	}
	if !strings.Contains(last, "completed all tasks") {
		t.Fatalf("all-done steering should say 'completed all tasks': %s", last)
	}
}

func TestTaskListTool_UpdateStaysMinimalWhenBlocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

	// A, B→A, C→B (chain A←B←C).
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

	// Mark A in_progress, then cancel C. Nothing else should be auto-advanced.
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
	// Tool response must not leak task list state (no descriptions, no "ready" summaries).
	for _, substr := range []string{"Task A", "Task B", "Task C", "No tasks are currently ready", "Next open task"} {
		if strings.Contains(updateRes.Output, substr) {
			t.Fatalf("tool response should not contain %q; got: %s", substr, updateRes.Output)
		}
	}
}

func TestSharedTaskStore_ChildUsesParentStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Create parent session and populate its task store.
	parentSess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession (parent): %v", err)
	}
	defer parentSess.Close()

	parentStore := parentSess.getOrCreateTaskStore()
	parentStore.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Shared task", Prompt: "Do shared work"},
	})

	// Create child session with a shared task store pointing to parent's store.
	childSess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		spawn: spawnConfig{sharedTaskStore: parentStore},
	})
	if err != nil {
		t.Fatalf("NewSession (child): %v", err)
	}
	defer childSess.Close()

	childStore := childSess.getOrCreateTaskStore()

	// Child should see parent's tasks.
	childTasks := childStore.View()
	if len(childTasks) != 1 {
		t.Fatalf("child expected 1 task from parent, got %d", len(childTasks))
	}
	if childTasks[0].Description != "Shared task" {
		t.Fatalf("child task description: got %q, want %q", childTasks[0].Description, "Shared task")
	}

	// Child adds a task — parent should see it too.
	childStore.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Child task", Prompt: "Child work"},
	})

	parentTasks := parentStore.View()
	if len(parentTasks) != 2 {
		t.Fatalf("parent expected 2 tasks after child append, got %d", len(parentTasks))
	}
}

func TestSharedTaskStore_ShareTasksWithChildrenConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Parent with ShareTasksWithChildren enabled.
	parentSess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ShareTasksWithChildren: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer parentSess.Close()

	// Populate parent task store.
	parentStore := parentSess.getOrCreateTaskStore()
	parentStore.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Shared via config", Prompt: "Work"},
	})

	// Exercise the actual propagation path inside prepareSubagentRun (subagents.go
	// lines 385-388). A re-implementation of the if-statement here would not catch
	// a condition inversion in the SUT.
	ctx := context.Background()
	prepared, err := parentSess.prepareSubagentRun(ctx, "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	// We are not launching the child; release the tree-counter slot that
	// prepareSubagentRun reserved (mirrors the spawnAgent in-process path).
	releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()

	// The child's spawn config must carry the parent's store — not a copy, not nil.
	// This assertion fails if the condition in prepareSubagentRun is ever inverted.
	if got := prepared.sub.sess.cfg.spawn.sharedTaskStore; got != parentStore {
		t.Fatalf("child cfg.spawn.sharedTaskStore = %v, want parent's store (%v)", got, parentStore)
	}

	// Cross-check via behaviour: the child's task-store view must contain the shared task.
	childTasks := prepared.sub.sess.getOrCreateTaskStore().View()
	if len(childTasks) != 1 || childTasks[0].Description != "Shared via config" {
		t.Fatalf("child should see parent task via ShareTasksWithChildren, got: %v", childTasks)
	}
}

func TestSharedTaskStore_IsolatedByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Parent with tasks.
	parentSess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession (parent): %v", err)
	}
	defer parentSess.Close()

	parentStore := parentSess.getOrCreateTaskStore()
	parentStore.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "Parent task", Prompt: "Parent work"},
	})

	// Child WITHOUT SharedTaskStore — should have its own empty store.
	childSess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession (child): %v", err)
	}
	defer childSess.Close()

	childStore := childSess.getOrCreateTaskStore()
	childTasks := childStore.View()
	if len(childTasks) != 0 {
		t.Fatalf("child expected 0 tasks (isolated), got %d", len(childTasks))
	}
}

func TestTaskListTool_AppendResponseIsMinimal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()
	env := sess.env

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

	// Append response is a terse acknowledgement: count added + progress.
	if !strings.Contains(appendRes.Output, "Added") {
		t.Fatalf("append response should acknowledge the added tasks: %s", appendRes.Output)
	}
	if !strings.Contains(appendRes.Output, "Progress") {
		t.Fatalf("append response should include Progress: %s", appendRes.Output)
	}
	// It MUST NOT leak the task list (descriptions, instructions, or start suggestions).
	for _, substr := range []string{"Task A", "Task B", "Next open task", "in_progress", "Instructions:"} {
		if strings.Contains(appendRes.Output, substr) {
			t.Fatalf("append response should not contain %q; got: %s", substr, appendRes.Output)
		}
	}
}

func TestTask_ReasoningEffort_RoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-effort")
	store.Load()

	added, err := store.Append([]taskpkg.TaskInput{{
		Type:            taskpkg.TaskTypeImplement,
		Description:     "plan the work",
		Prompt:          "think carefully",
		ReasoningEffort: "xhigh",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].ReasoningEffort != "xhigh" {
		t.Errorf("got %q, want xhigh", added[0].ReasoningEffort)
	}

	// Reload from disk and verify persistence.
	store2 := taskpkg.NewTaskStore(dir, "test-effort")
	store2.Load()
	tasks := store2.View()
	if tasks[0].ReasoningEffort != "xhigh" {
		t.Errorf("after reload: got %q, want xhigh", tasks[0].ReasoningEffort)
	}
}

func TestTask_Insert_RoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-insert")
	store.Load()

	added, err := store.Append([]taskpkg.TaskInput{{
		Type:        taskpkg.TaskTypeImplement,
		Description: "placeholder",
		Prompt:      "do the work",
		Insert:      "parent_tasks",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].Insert != "parent_tasks" {
		t.Errorf("got %q, want parent_tasks", added[0].Insert)
	}

	// Reload from disk and verify that Insert survives serialization.
	// A json:"-" tag (or missing field) would drop the value silently.
	store2 := taskpkg.NewTaskStore(dir, "test-insert")
	store2.Load()
	tasks := store2.View()
	if len(tasks) == 0 {
		t.Fatal("after reload: no tasks found")
	}
	if tasks[0].Insert != "parent_tasks" {
		t.Errorf("after reload: got %q, want parent_tasks", tasks[0].Insert)
	}
}

func TestTaskStore_CurrentInProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := taskpkg.NewTaskStore(dir, "test-current")
	store.Load()

	store.Append([]taskpkg.TaskInput{
		{Type: taskpkg.TaskTypeResearch, Description: "first", Prompt: "do first", ReasoningEffort: "low"},
		{Type: taskpkg.TaskTypeResearch, Description: "second", Prompt: "do second", ReasoningEffort: "xhigh"},
	})

	// No task in progress yet.
	_, ok := store.CurrentInProgress()
	if ok {
		t.Fatal("expected no in_progress task")
	}

	// Start first task.
	store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}})
	task, ok := store.CurrentInProgress()
	if !ok || task.ID != 1 {
		t.Fatalf("expected task 1 in progress, got ok=%v task=%+v", ok, task)
	}
	if task.ReasoningEffort != "low" {
		t.Errorf("effort = %q, want low", task.ReasoningEffort)
	}

	// Complete first, start second.
	store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskDone}, {ID: 2, Status: taskpkg.TaskInProgress}})
	task, ok = store.CurrentInProgress()
	if !ok || task.ID != 2 {
		t.Fatalf("expected task 2 in progress, got ok=%v task=%+v", ok, task)
	}
	if task.ReasoningEffort != "xhigh" {
		t.Errorf("effort = %q, want xhigh", task.ReasoningEffort)
	}
}
