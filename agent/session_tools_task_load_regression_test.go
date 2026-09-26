package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

func taskToolRegistryWithSchedulingHooks(store *taskpkg.TaskStore, beforeView, afterReload func()) *tool.Registry {
	deps := &toolDeps{
		emit:           func(events.EventKind, events.EventData) {},
		steer:          func(string, string) error { return nil },
		resultToolName: func() string { return "communicate" },
		taskGuard: taskGuard{
			getOrCreateTaskStore:  func() *taskpkg.TaskStore { return store },
			markUsed:              func() {},
			beforeBareTaskView:    beforeView,
			afterFailedTaskReload: afterReload,
		},
	}
	reg := tool.NewRegistry()
	registerTaskTools(reg, deps)
	return reg
}

func taskToolSchedulingHook(t *testing.T, name string) (hook, wait, release func()) {
	t.Helper()
	fired := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
	hook = func() {
		close(fired)
		<-released
	}
	wait = func() {
		t.Helper()
		// TRIPWIRE: five seconds is far above the expected in-process hook signal;
		// only a missing scheduling signal should reach this timeout.
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-fired:
		case <-timer.C:
			release()
			t.Fatalf("%s scheduling hook did not fire", name)
		}
	}
	return hook, wait, release
}

func seededTaskToolStore(t *testing.T, sessionID string) (afero.Fs, *taskpkg.TaskStore, []byte) {
	t.Helper()
	base := afero.NewMemMapFs()
	store := taskpkg.NewTaskStore("/state", sessionID).SetFs(base)
	if _, err := store.Append([]taskpkg.TaskInput{{Description: "durable", Prompt: "keep"}}); err != nil {
		t.Fatal(err)
	}
	original, err := afero.ReadFile(base, "/state/tasks/"+sessionID+".json")
	if err != nil {
		t.Fatal(err)
	}
	return base, store, original
}

func executeTaskToolAfterHook(t *testing.T, h *taskToolHarness, waitForHook func()) <-chan tool.ExecResult {
	t.Helper()
	done := make(chan tool.ExecResult, 1)
	go func() { done <- h.call(t, map[string]any{}) }()
	waitForHook()
	return done
}

func TestTaskTool_ViewRejectsConcurrentFailedReload(t *testing.T) {
	base, store, original := seededTaskToolStore(t, "view-race")
	beforeView, waitForHook, release := taskToolSchedulingHook(t, "bare-view")
	reg := taskToolRegistryWithSchedulingHooks(store, beforeView, nil)
	h := newTaskToolHarness(t, nil)
	h.store = store
	h.reg = reg
	done := executeTaskToolAfterHook(t, h, waitForHook)
	store.SetFs(fault.FS(base, fault.FromBytes([]byte{0})))
	if err := store.Load(); err == nil {
		t.Fatal("competing failed Load unexpectedly succeeded")
	}
	view, viewErr := store.ViewWithError()
	if len(view) != 1 || view[0].Description != "durable" || !errors.Is(viewErr, taskpkg.ErrTaskStoreUnavailable) {
		t.Fatalf("post-failure store state = tasks=%#v err=%v", view, viewErr)
	}
	got, err := afero.ReadFile(base, "/state/tasks/view-race.json")
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed competing Load changed bytes: got=%q err=%v want=%q", got, err, original)
	}
	release()
	result := <-done
	if !result.IsError || !errors.Is(result.Err, taskpkg.ErrTaskStoreUnavailable) {
		t.Fatalf("task_list result = error=%v err=%v output=%q; want unavailable error", result.IsError, result.Err, result.Output)
	}
}

func TestTaskTool_FailedReloadRetainsErrorAfterConcurrentRecovery(t *testing.T) {
	base, store, original := seededTaskToolStore(t, "reload-race")
	initialCause := fault.ErrInjected
	retryCause := io.ErrUnexpectedEOF
	failing := fault.FS(base, fault.FromBytes([]byte{0, 4}))
	store.SetFs(failing)
	initialErr := store.Load()
	if initialErr == nil || !errors.Is(initialErr, initialCause) || errors.Is(initialErr, retryCause) {
		t.Fatal("initial failed Load unexpectedly succeeded")
	}
	afterReload, waitForHook, release := taskToolSchedulingHook(t, "failed-reload")
	reg := taskToolRegistryWithSchedulingHooks(store, nil, afterReload)
	h := newTaskToolHarness(t, nil)
	h.store = store
	h.reg = reg
	done := executeTaskToolAfterHook(t, h, waitForHook)
	store.SetFs(base)
	if err := store.Load(); err != nil {
		release()
		t.Fatal(err)
	}
	if err := store.LoadError(); err != nil {
		release()
		t.Fatalf("successful competing recovery left fence: %v", err)
	}
	release()
	result := <-done
	if !result.IsError || !errors.Is(result.Err, taskpkg.ErrTaskStoreUnavailable) || !errors.Is(result.Err, retryCause) || errors.Is(result.Err, initialCause) {
		t.Fatalf("task_list result = error=%v err=%v output=%q; want unavailable wrapping retry cause %v, not initial cause %v (initial=%v)", result.IsError, result.Err, result.Output, retryCause, initialCause, initialErr)
	}
	recoveredBytes, err := afero.ReadFile(base, "/state/tasks/reload-race.json")
	if err != nil || !bytes.Equal(recoveredBytes, original) {
		t.Fatalf("successful recovery changed durable bytes: got=%q err=%v want=%q", recoveredBytes, err, original)
	}
	healthy := h.call(t, map[string]any{})
	var wantTasks, gotTasks []taskpkg.Task
	if err := json.Unmarshal(original, &wantTasks); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(healthy.ToolState, &gotTasks); err != nil {
		t.Fatalf("decode recovered task state: %v; raw=%s", err, healthy.ToolState)
	}
	if healthy.IsError || healthy.Err != nil || !strings.Contains(healthy.Output, "durable") || !reflect.DeepEqual(gotTasks, wantTasks) {
		t.Fatalf("healthy task_list after recovery = error=%v err=%v output=%q state=%#v; want fixture tasks=%#v", healthy.IsError, healthy.Err, healthy.Output, gotTasks, wantTasks)
	}
}

func TestSession_TaskToolsRejectAfterFailedTaskLoad(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := taskpkg.NewTaskStore("/state", "failed-session").SetFs(base)
	original := []byte("{malformed task json")
	if err := afero.WriteFile(base, storePathForTest(), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err == nil {
		t.Fatal("Load unexpectedly accepted malformed JSON")
	}

	h := newTaskToolHarness(t, nil)
	h.store = store
	for name, args := range map[string]map[string]any{
		"view":   nil,
		"add":    {"add": []any{map[string]any{"type": "implement", "description": "overwrite", "prompt": "prompt"}}},
		"update": {"update": []any{map[string]any{"id": float64(1), "status": "done"}}},
	} {
		t.Run(name, func(t *testing.T) {
			result := h.call(t, args)
			if result.Err == nil || !strings.Contains(strings.ToLower(result.Err.Error()), "unavailable") {
				t.Fatalf("task_list %s error = %v, output=%q; want unavailable fence", name, result.Err, result.Output)
			}
			got, err := afero.ReadFile(base, storePathForTest())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("task_list %s replaced failed-load bytes: got %q, want %q", name, got, original)
			}
		})
	}
}

func TestSession_TasksWithErrorClearsInitialLoadErrorAfterStoreRepair(t *testing.T) {
	t.Parallel()
	// TasksWithError's contract treats nil plus an empty slice as authoritative
	// state; a non-nil error means the aggregate is unavailable to envelopes.
	base := afero.NewMemMapFs()
	store := taskpkg.NewTaskStore("/state", "repaired-session").SetFs(base)
	if err := afero.WriteFile(base, "/state/tasks/repaired-session.json", []byte("{malformed"), 0o644); err != nil {
		t.Fatal(err)
	}
	initialErr := store.Load()
	if initialErr == nil {
		t.Fatal("initial malformed Load unexpectedly succeeded")
	}
	if err := afero.WriteFile(base, "/state/tasks/repaired-session.json", []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.SetFs(base) // external repair: the file is readable again.
	s := newTestSession(t)
	s.taskStore = store
	s.taskStoreOnce.Do(func() {})
	reg := tool.NewRegistry()
	registerTaskTools(reg, newToolDeps(s))
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID: "repaired-empty-store", Name: "task_list", Arguments: args,
	})
	if result.IsError {
		t.Fatalf("real task_list after external repair = %q; want recovery", result.Output)
	}
	tasks, err := s.TasksWithError()
	if len(tasks) != 0 || err != nil {
		t.Fatalf("TasksWithError after successful existing reload = tasks=%v err=%v; want authoritative empty snapshot with nil error", tasks, err)
	}
}

func TestSession_TaskListRecoversAfterTransientLoadFailure(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	path := "/state/tasks/transient-session.json"
	seed := taskpkg.NewTaskStore("/state", "transient-session").SetFs(base)
	if _, err := seed.Append([]taskpkg.TaskInput{{Description: "survives recovery", Prompt: "keep me"}}); err != nil {
		t.Fatal(err)
	}
	original, err := afero.ReadFile(base, path)
	if err != nil {
		t.Fatal(err)
	}
	store := taskpkg.NewTaskStore("/state", "transient-session").SetFs(base)

	// Model getOrCreateTaskStore's one initial Load failing transiently. Consume
	// the session's once so the later call below is the real task_list executor,
	// not another store construction or a direct recovery Load in the assertion.
	store.SetFs(fault.FS(base, fault.FromBytes([]byte{0})))
	if err := store.Load(); err == nil {
		t.Fatal("transient initial Load unexpectedly succeeded")
	}
	if got, err := afero.ReadFile(base, path); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed initial Load changed original bytes: got %q, err=%v", got, err)
	}
	store.SetFs(base) // external repair: the file is readable again.

	s := newTestSession(t)
	s.taskStore = store
	s.taskStoreOnce.Do(func() {})
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "transient-load-recovery", Name: "task_list", Arguments: args,
	})
	if result.IsError {
		t.Fatalf("real task_list after external repair = %q; want recovery", result.Output)
	}
	if err := store.LoadError(); err != nil {
		t.Fatalf("store.LoadError after recovered task_list = %v, want nil", err)
	}
	tasks, err := s.TasksWithError()
	if err != nil || len(tasks) != 1 || tasks[0].Description != "survives recovery" {
		t.Fatalf("TasksWithError after recovered task_list = tasks=%v err=%v; want recovered authoritative task snapshot", tasks, err)
	}
	if got, err := afero.ReadFile(base, path); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("recovered task_list changed original bytes: got %q, err=%v", got, err)
	}
}

func TestSession_SharedTaskListRecoveryClearsOwnerLoadError(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	seed := taskpkg.NewTaskStore("/state", "shared-recovery").SetFs(base)
	if _, err := seed.Append([]taskpkg.TaskInput{{Description: "shared task", Prompt: "survives child repair"}}); err != nil {
		t.Fatal(err)
	}
	store := taskpkg.NewTaskStore("/state", "shared-recovery").SetFs(base)
	store.SetFs(fault.FS(base, fault.FromBytes([]byte{0})))
	initialErr := store.Load()
	if initialErr == nil {
		t.Fatal("initial transient Load unexpectedly succeeded")
	}
	store.SetFs(base) // The descendant repairs the shared store externally.

	owner := newTestSession(t)
	owner.taskStore = store
	owner.taskStoreOnce.Do(func() {})
	child := newTestSession(t)
	child.taskStore = store
	child.taskStoreOnce.Do(func() {})

	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	childReg := tool.NewRegistry()
	registerTaskTools(childReg, newToolDeps(child))
	childResult := childReg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID: "shared-child-repair", Name: "task_list", Arguments: args,
	})
	if childResult.IsError {
		t.Fatalf("descendant task_list recovery = %q; want success", childResult.Output)
	}
	if tasks, err := owner.TasksWithError(); len(tasks) != 1 || tasks[0].Description != "shared task" || err != nil {
		t.Fatalf("owner TasksWithError immediately after child recovery = tasks=%v err=%v; want recovered authoritative task snapshot", tasks, err)
	}

	ownerReg := tool.NewRegistry()
	registerTaskTools(ownerReg, newToolDeps(owner))
	ownerResult := ownerReg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID: "shared-owner-recovery", Name: "task_list", Arguments: args,
	})
	if ownerResult.IsError {
		t.Fatalf("owner task_list after shared recovery = %q; want success", ownerResult.Output)
	}
	if tasks, err := owner.TasksWithError(); len(tasks) != 1 || tasks[0].Description != "shared task" || err != nil {
		t.Fatalf("owner TasksWithError after owner task_list = tasks=%v err=%v; want recovered authoritative task snapshot", tasks, err)
	}
}

func storePathForTest() string {
	return "/state/tasks/failed-session.json"
}
