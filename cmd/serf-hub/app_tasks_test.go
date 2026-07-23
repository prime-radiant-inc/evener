package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

// taskListSource is a minimal appsource.Source test double that stubs the
// live path's ListTasks response; every other (large) Source method falls
// through to the embedded relayLifecycleSource's stub implementation
// (app_rpc_test.go), mirroring listThreadSource's existing override pattern.
type taskListSource struct {
	relayLifecycleSource
	id        string
	tasksResp appwire.TaskListResponse
	tasksErr  error
}

func (s *taskListSource) ID() string { return s.id }

func (s *taskListSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return s.tasksResp, s.tasksErr
}

// newExitedLocalRegistry returns a registry whose "local" source is the real
// LocalDaemonSource with zero live rendezvous entries — exactly what
// entryForRef sees once a session's daemon process has exited, i.e. the
// production error path, not an assumed shape.
func newExitedLocalRegistry() *appsource.Registry {
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return nil }, http.DefaultClient))
	return sources
}

// seedPastSessionWithTasks builds a past-indexed session (project state dir +
// meta.json, mirroring app_threadread_test.go's fixture convention). When
// tasks is non-nil, it also writes the session's persisted task file through
// a real task.TaskStore (task.NewTaskStore + Load + Append) so the fixture's
// on-disk JSON can never drift from what agent/task actually writes.
func seedPastSessionWithTasks(t *testing.T, tasks []task.TaskInput) (hubcore.WebConfig, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-tasks-0000000000")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if tasks != nil {
		store := task.NewTaskStore(stateDir, sessionID)
		if err := store.Load(); err != nil {
			t.Fatal(err)
		}
		store.SetClock(func() time.Time { return now })
		if _, err := store.Append(tasks); err != nil {
			t.Fatal(err)
		}
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, sessionID, stateDir
}

// TestHubTasksList_ServesPersistedTasksForExitedSession is the RED case: a
// session whose daemon has exited (no live rendezvous entry) must still serve
// its real persisted tasks from <StateDir>/tasks/<id>.json, not the
// SessionUnavailable error entryForRef raises for a live-only lookup.
func TestHubTasksList_ServesPersistedTasksForExitedSession(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "ship the fix", Prompt: "do it"},
	})
	sources := newExitedLocalRegistry()

	resp, err := hubTasksList(context.Background(), cfg, sources, appwire.TaskListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubTasksList: %v", err)
	}
	tasks, ok := resp.Data.([]task.Task)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want []task.Task", resp.Data, resp.Data)
	}
	if len(tasks) != 1 || tasks[0].Description != "ship the fix" {
		t.Fatalf("tasks = %+v, want one task %q", tasks, "ship the fix")
	}
}

// TestHubTasksList_AbsentTaskFileIsEmptySuccess proves a known past session
// that never created a task returns a success with an empty list — matching
// task.TaskStore.View() on a loaded-but-never-populated store — rather than
// an error, and that the wire shape is `[]`, not `null` (a live TasksList
// response's own empty shape; see agent.Session.Tasks/TaskStore.View).
func TestHubTasksList_AbsentTaskFileIsEmptySuccess(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	sources := newExitedLocalRegistry()

	resp, err := hubTasksList(context.Background(), cfg, sources, appwire.TaskListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubTasksList: %v", err)
	}
	tasks, ok := resp.Data.([]task.Task)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want []task.Task", resp.Data, resp.Data)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want empty", tasks)
	}
	encoded, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("marshal resp.Data: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("resp.Data marshaled = %s, want []", encoded)
	}
}

// TestHubTasksList_UnknownRefReturnsNotFoundError proves a ref the past index
// has never heard of (never indexed, not merely never-loaded) still surfaces
// the original SessionUnavailable thread-not-found error: the past path never
// serves a task file for a session the hub cannot otherwise account for.
func TestHubTasksList_UnknownRefReturnsNotFoundError(t *testing.T) {
	root := t.TempDir()
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	sources := newExitedLocalRegistry()

	_, err = hubTasksList(context.Background(), cfg, sources, appwire.TaskListParams{Ref: "local:" + sessionID})
	if !isSessionUnavailableError(err) {
		t.Fatalf("err = %v, want a SessionUnavailable thread-not-found error", err)
	}
}

// TestHubTasksList_LiveSourceTakesPrecedenceOverPast proves a running
// daemon's task store is authoritative: even though a past index entry (with
// its own, different, persisted task) exists for the same session, a
// successful live ListTasks response is returned untouched and past is never
// consulted.
func TestHubTasksList_LiveSourceTakesPrecedenceOverPast(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "stale past task", Prompt: "do it"},
	})
	liveTasks := []task.Task{{ID: 1, Type: task.TaskTypeImplement, Description: "live task", Status: task.TaskOpen}}
	sources := appsource.NewRegistry()
	sources.Add(&taskListSource{id: "local", tasksResp: appwire.TaskListResponse{Data: liveTasks}})

	resp, err := hubTasksList(context.Background(), cfg, sources, appwire.TaskListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubTasksList: %v", err)
	}
	tasks, ok := resp.Data.([]task.Task)
	if !ok || len(tasks) != 1 || tasks[0].Description != "live task" {
		t.Fatalf("resp.Data = %#v, want the live task (past must not be consulted)", resp.Data)
	}
}

// TestHubTasksList_TransientLiveErrorIsNotMaskedByPast proves a live daemon
// error that is NOT the SessionUnavailable dead-session condition (here a
// generic InternalError) is returned unchanged, even though a matching past
// index entry with a persisted task exists: a running daemon's transient
// failure must never be silently masked by a stale past read.
func TestHubTasksList_TransientLiveErrorIsNotMaskedByPast(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "stale past task", Prompt: "do it"},
	})
	sources := appsource.NewRegistry()
	sources.Add(&taskListSource{id: "local", tasksErr: appwire.InternalError("boom")})

	_, err := hubTasksList(context.Background(), cfg, sources, appwire.TaskListParams{Ref: "local:" + sessionID})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError || wire.Message != "boom" {
		t.Fatalf("err = %v, want the live source's own InternalError(\"boom\") unmasked", err)
	}
}
