package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// hubTasksList answers serf/tasks/list. A running daemon's in-memory task
// store is authoritative, so it is always tried first. Only the specific
// dead-session condition — the daemon process has exited, surfaced uniformly
// as SessionUnavailable whether entryForRef's live-rendezvous scan misses the
// thread or a dial to a stale entry fails — falls back to the persisted
// <StateDir>/tasks/<id>.json past path (mirroring pastThreadForRead in
// app_threadread.go). Any other error, including a transient failure talking
// to a daemon that IS running, is returned unchanged: falling back on it
// would silently mask a real error behind a stale or empty past read.
func hubTasksList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	var resp appwire.TaskListResponse
	if err == nil {
		resp, err = source.ListTasks(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isSessionUnavailableError(err) {
		return appwire.TaskListResponse{}, err
	}
	pastResp, ok, pastErr := pastTasksListResponse(cfg, params)
	if pastErr != nil {
		return appwire.TaskListResponse{}, pastErr
	}
	if ok {
		return pastResp, nil
	}
	return appwire.TaskListResponse{}, err
}

// pastTasksListResponse mirrors pastThreadForRead's past-index gating
// (app_threadread.go): it resolves ref to a local past thread id and requires
// that id to already be known to the past index before serving anything, so a
// task file is never returned for a session the hub cannot otherwise account
// for. Only then does it read the session's persisted task file.
func pastTasksListResponse(cfg hubcore.WebConfig, params appwire.TaskListParams) (appwire.TaskListResponse, bool, error) {
	if cfg.Past == nil {
		return appwire.TaskListResponse{}, false, nil
	}
	threadID, ok := localPastThreadID(appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.TaskListResponse{}, false, nil
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.TaskListResponse{}, false, nil
	}
	tasks, err := loadPersistedTasks(entry.StateDir, entry.Meta.ID)
	if err != nil {
		return appwire.TaskListResponse{}, true, err
	}
	return appwire.TaskListResponse{Data: tasks}, true, nil
}

// loadPersistedTasks reads a session's task list straight from
// <stateDir>/tasks/<sessionID>.json — the file task.TaskStore.save writes on
// every mutation (agent/task/task_store.go) — decoding into task.Task itself
// so the wire shape can never drift from what a live daemon's TasksList
// (agent.Session.Tasks, which serves TaskStore.View()) returns. A missing
// file is success with an empty, non-nil slice: that is exactly what
// TaskStore.View() returns for a store that was loaded but never populated,
// i.e. an ended session that never created a task.
func loadPersistedTasks(stateDir, sessionID string) ([]task.Task, error) {
	path := filepath.Join(stateDir, "tasks", sessionID+".json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []task.Task{}, nil
	}
	if err != nil {
		return nil, err
	}
	tasks := []task.Task{}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}
