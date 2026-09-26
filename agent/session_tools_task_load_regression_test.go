package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/afero"
	taskpkg "primeradiant.com/evener/agent/task"
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

func TestSession_TasksWithErrorRetainsInitialLoadErrorAfterStoreRepair(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := taskpkg.NewTaskStore("/state", "repaired-session").SetFs(base)
	if err := afero.WriteFile(base, "/state/tasks/repaired-session.json", []byte("{malformed"), 0o644); err != nil {
		t.Fatal(err)
	}
	initialErr := store.Load()
	if initialErr == nil {
		t.Fatal("initial malformed Load unexpectedly succeeded")
	}
	s := &Session{taskStore: store, taskStoreLoadErr: initialErr}
	s.taskStoreOnce.Do(func() {})
	if err := afero.WriteFile(base, "/state/tasks/repaired-session.json", []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("repair Load: %v", err)
	}
	if err := store.LoadError(); err != nil {
		t.Fatalf("store.LoadError after repair = %v, want nil", err)
	}
	tasks, err := s.TasksWithError()
	if len(tasks) != 0 || err == nil {
		t.Fatalf("TasksWithError after successful existing reload = tasks=%v err=%v; want empty snapshot plus initial aggregate error", tasks, err)
	}
}

func storePathForTest() string {
	return "/state/tasks/failed-session.json"
}
