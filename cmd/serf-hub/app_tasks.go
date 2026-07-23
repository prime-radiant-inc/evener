package main

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// hubTasksList answers serf/tasks/list. A running daemon's in-memory task
// store is authoritative, so it is always tried first. Only the specific
// dead-session condition — entryForRef finding no live rendezvous entry for
// the thread (cmd/serf-hub/internal/appsource/local_daemon.go:551, the sole
// SessionUnavailable emitter that uses the "thread not found: " message) —
// falls back to the persisted <StateDir>/tasks/<id>.json past path (mirroring
// pastThreadForRead in app_threadread.go).
//
// The SessionUnavailable wire shape alone is NOT a safe gate for this:
// localDaemonDialError/localDaemonCallError (local_daemon.go:428-504) raise
// the identical Code+SerfErrorInfo for a transient dial/call failure against
// a LIVE entry — i.e. a daemon that has NOT exited (ECONNREFUSED, ECONNRESET,
// EPIPE, EOF, timeouts, websocket-close) — so isSessionUnavailableError alone
// would also match those and silently mask a real, retryable connectivity
// error behind a stale or empty past read. isDeadSessionTasksError adds the
// message-prefix check that only entryForRef's dead-session error satisfies.
func hubTasksList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	var resp appwire.TaskListResponse
	if err == nil {
		resp, err = source.ListTasks(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isDeadSessionTasksError(err) {
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

// threadNotFoundMessagePrefix is the exact message entryForRef
// (cmd/serf-hub/internal/appsource/local_daemon.go:551) uses for its
// SessionUnavailable error — the only SessionUnavailable emitter in that file
// meaning "there is no live rendezvous entry for this thread" (a genuinely
// dead session). Every other SessionUnavailable emitter there
// (localDaemonDialError, localDaemonCallError: ECONNREFUSED/ECONNRESET/EPIPE/
// EOF/timeouts/websocket-close against a still-live entry) uses a different
// message, "local daemon unavailable: ...".
const threadNotFoundMessagePrefix = "thread not found: "

// isDeadSessionTasksError reports whether err is specifically entryForRef's
// dead-session condition, as opposed to any other SessionUnavailable-shaped
// error. isSessionUnavailableError (app_compact.go) checks only Code and
// SerfErrorInfo, which both the dead-session error and a transient dial/call
// failure share; only the message tells them apart. Other callers of
// isSessionUnavailableError (app_compact.go/app_model.go) gate an ACTIVE
// recovery attempt (hubThreadResume) that fails loudly if the predicate is
// wrong; this gates a PASSIVE disk read that fails silently if wrong, so it
// needs the narrower, unambiguous signal.
func isDeadSessionTasksError(err error) bool {
	if !isSessionUnavailableError(err) {
		return false
	}
	var wire appwire.WireError
	errors.As(err, &wire)
	return strings.HasPrefix(wire.Message, threadNotFoundMessagePrefix)
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

// loadPersistedTasks reads a session's task list from
// <stateDir>/tasks/<sessionID>.json through agent/task's own TaskStore
// (Load+View) — the same reader a resumed daemon's getOrCreateTaskStore uses
// (agent/session_tools.go) — instead of a hand-rolled parser, so this path
// inherits TaskStore.Load's not-exist-is-empty semantics and decode-error
// handling directly, and so the restart-parity fuzz coverage
// (FuzzTaskStoreLoad, FuzzTaskStorePersistence's requireSameReload) applies
// to this shipped path rather than to a hand-copied sibling of it. A missing
// file is success with an empty, non-nil slice — TaskStore.View() on a
// loaded-but-never-populated store, matching a live daemon's own empty-task-
// list shape. Any other read or decode error propagates as a real error.
func loadPersistedTasks(stateDir, sessionID string) ([]task.Task, error) {
	store := task.NewTaskStore(stateDir, sessionID)
	if err := store.Load(); err != nil {
		return nil, err
	}
	return store.View(), nil
}
