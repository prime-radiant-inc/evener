package task

import (
	"testing"

	"github.com/spf13/afero"
)

func TestTaskStoreLoadMissingFileClearsCachedState(t *testing.T) {
	fs := afero.NewMemMapFs()
	store := NewTaskStore("/state", "missing-reload").SetFs(fs)
	added, err := store.Append([]TaskInput{{Description: "removed", Prompt: "not retained"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(store.path); err != nil {
		t.Fatal(err)
	}

	if err := store.Load(); err != nil {
		t.Fatalf("Load after external removal: %v", err)
	}
	if got := store.View(); len(got) != 0 {
		t.Fatalf("View after missing-file Load = %#v, want empty", got)
	}

	fresh, err := store.Append([]TaskInput{{Description: "fresh", Prompt: "new state"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != added[0].ID {
		t.Fatalf("first task after missing-file Load = %#v, want ID %d", fresh, added[0].ID)
	}
}

func TestTaskStoreLoadNormalizesReasoningEffortAliases(t *testing.T) {
	fs := afero.NewMemMapFs()
	store := NewTaskStore("/state", "effort-aliases").SetFs(fs)
	if err := afero.WriteFile(fs, store.path, []byte(`[
  {"id":1,"description":"disabled","prompt":"p","status":"open","reasoning_effort":" off "},
  {"id":2,"description":"disabled via false","prompt":"p","status":"open","reasoning_effort":"false"},
  {"id":3,"description":"disabled via zero","prompt":"p","status":"open","reasoning_effort":"0"},
  {"id":4,"description":"disabled via null","prompt":"p","status":"open","reasoning_effort":"null"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	for _, task := range store.View() {
		if task.ReasoningEffort != "none" {
			t.Fatalf("task %d reasoning_effort = %q, want none", task.ID, task.ReasoningEffort)
		}
	}
}
