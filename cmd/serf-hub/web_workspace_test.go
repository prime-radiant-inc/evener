package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/task"
)

// TestRenderSessionTasks_PastFallbackMatchesTaskStoreLoader characterizes
// renderSessionTasks's persisted-tasks fallback for an exited session (no
// live daemon): the JSON it serves must match exactly what a fresh
// task.TaskStore.Load()+View() over the same file produces — the schema-
// sourced reader the serf/tasks/list RPC fallback already uses
// (hubTasksList/loadPersistedTasks, app_tasks.go). Pins the shared behavior
// so the jx9e consolidation (routing this handler through the same reader)
// cannot silently change what's rendered for a normal, well-formed fixture.
// seedPastSessionWithTasks is app_tasks_test.go's fixture helper (same
// package); it writes the task file through a real task.TaskStore so the
// fixture can never drift from what agent/task actually persists.
func TestRenderSessionTasks_PastFallbackMatchesTaskStoreLoader(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "ship the fix", Prompt: "do it"},
		{Type: task.TaskTypeVerify, Description: "check it", Prompt: "verify it"},
	})
	web := NewWebServer(cfg)

	rec := httptest.NewRecorder()
	web.renderSessionTasks(rec, httptest.NewRequest(http.MethodGet, "/", nil), sessionID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got []task.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not a valid task.Task array: %v\nbody: %s", err, rec.Body.String())
	}

	store := task.NewTaskStore(stateDir, sessionID)
	if err := store.Load(); err != nil {
		t.Fatalf("reference TaskStore.Load: %v", err)
	}
	want := store.View()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renderSessionTasks body = %+v, want TaskStore.View() = %+v", got, want)
	}
}

// TestRenderSessionTasks_AbsentTaskFileRendersEmptyArray pins the existing
// "no task file yet" contract (web_workspace.go's own doc comment: "A missing
// file or absent session returns an empty array (200)") across the
// consolidation: a known past session that never created a task still
// renders `[]`, matching task.TaskStore.View() on a loaded-but-never-
// populated store.
func TestRenderSessionTasks_AbsentTaskFileRendersEmptyArray(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	web := NewWebServer(cfg)

	rec := httptest.NewRecorder()
	web.renderSessionTasks(rec, httptest.NewRequest(http.MethodGet, "/", nil), sessionID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got []task.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("tasks = %+v, want empty", got)
	}
}

// TestRenderSessionTasks_CorruptTaskFileSurfacesAsErrorNotRawPassthrough is
// the RED case for the jx9e consolidation: the old hand-rolled disk-fallback
// parser (os.ReadFile + a raw w.Write of whatever bytes are on disk) never
// validates the file's content as JSON, so a corrupted/truncated tasks file
// was served verbatim as a 200 "success" body — forwarding the torn bytes to
// the client under Content-Type: application/json. The schema-sourced
// task.TaskStore.Load()+View() reader the RPC fallback already uses
// (loadPersistedTasks, app_tasks.go) surfaces this as a real decode error
// instead (mirrored by TestHubTasksList_CorruptTaskFileReturnsErrorNotEmpty
// Success in app_tasks_test.go). Consolidating onto that reader must make
// this handler do the same: a real error, never a raw-bytes 200.
func TestRenderSessionTasks_CorruptTaskFileSurfacesAsErrorNotRawPassthrough(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, nil)
	tasksPath := filepath.Join(stateDir, "tasks", sessionID+".json")
	if err := os.MkdirAll(filepath.Dir(tasksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const tornMarker = "torn-mid-array-marker"
	// Truncated mid-array: a torn write, not merely empty or absent.
	if err := os.WriteFile(tasksPath, []byte(`[{"id":1,"description":"`+tornMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(cfg)

	rec := httptest.NewRecorder()
	web.renderSessionTasks(rec, httptest.NewRequest(http.MethodGet, "/", nil), sessionID)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200, want a real error status for a torn/corrupt task file (body: %s)", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), tornMarker) {
		t.Fatalf("response body echoes the raw corrupt file content verbatim: %s", rec.Body.String())
	}
}
