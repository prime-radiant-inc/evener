package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

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
	s.taskStoreLoadErr = initialErr
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
	s.taskStoreLoadErr = store.LoadError()
	s.taskStoreOnce.Do(func() {})
	reg := tool.NewRegistry()
	registerTaskTools(reg, newToolDeps(s))
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
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

func storePathForTest() string {
	return "/state/tasks/failed-session.json"
}
