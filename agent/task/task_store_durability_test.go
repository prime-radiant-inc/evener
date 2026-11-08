package task

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/fsfake"
	"primeradiant.com/evener/execsupport/fsatomic"
)

func TestTaskStoreSaveSyncsFileAndDirectory(t *testing.T) {
	t.Parallel()
	fs := fsfake.NewSyncFS(afero.NewMemMapFs())
	store := NewTaskStore("/state", "session-1").SetFs(fs)
	if err := store.saveTasks([]Task{{ID: 1, Status: TaskOpen, Description: "task"}}); err != nil {
		t.Fatalf("saveTasks: %v", err)
	}
	tasksDir := "/state/tasks"
	if !fs.SawSyncUnder(tasksDir) {
		t.Fatalf("task save did not sync its temp file; operations: %v", fs.Ops())
	}
	if !fs.SawSync(tasksDir) {
		t.Fatalf("task save did not sync its directory; operations: %v", fs.Ops())
	}
}

func TestTaskStoreSaveDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := NewTaskStore("/state", "session-1").SetFs(&dirOpenFailFS{Fs: base, dir: "/state/tasks"})
	want := []Task{{ID: 1, Status: TaskOpen, Description: "published"}}
	err := store.saveTasks(want)
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("saveTasks error = %v, want fsatomic.ErrPublished", err)
	}
	data, readErr := afero.ReadFile(base, "/state/tasks/session-1.json")
	if readErr != nil {
		t.Fatalf("read published tasks: %v", readErr)
	}
	var got []Task
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode published tasks: %v", err)
	}
	if len(got) != 1 || got[0].Description != "published" {
		t.Fatalf("published tasks = %#v", got)
	}
}

func TestTaskStoreAppendAdoptsPublishedSnapshot(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := NewTaskStore("/state", "session-1").SetFs(&dirOpenFailFS{Fs: base, dir: "/state/tasks"})
	added, err := store.Append([]TaskInput{{Description: "published"}})
	if !errors.Is(err, fsatomic.ErrPublished) || len(added) != 1 {
		t.Fatalf("Append with a directory-sync failure = %#v, %v", added, err)
	}
	if view := store.View(); len(view) != 1 || view[0].Description != "published" {
		t.Fatalf("in-memory state after published Append = %#v", view)
	}

	// A later save built from the adopted state must not drop the published
	// task out of the file.
	store.SetFs(base)
	if err := store.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}}); err != nil {
		t.Fatalf("Update after published Append: %v", err)
	}
	reloaded := NewTaskStore("/state", "session-1").SetFs(base)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load after published Append: %v", err)
	}
	view := reloaded.View()
	if len(view) != 1 || view[0].Status != TaskDone {
		t.Fatalf("reloaded tasks after published Append = %#v", view)
	}
}

func TestTaskStoreUpdateAdoptsPublishedSnapshot(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := NewTaskStore("/state", "session-1").SetFs(base)
	added, err := store.Append([]TaskInput{{Description: "first"}})
	if err != nil || len(added) != 1 {
		t.Fatalf("seed Append = %#v, %v", added, err)
	}

	store.SetFs(&dirOpenFailFS{Fs: base, dir: "/state/tasks"})
	err = store.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}})
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("Update with a directory-sync failure = %v, want fsatomic.ErrPublished", err)
	}
	if view := store.View(); len(view) != 1 || view[0].Status != TaskDone {
		t.Fatalf("in-memory state after published Update = %#v", view)
	}

	store.SetFs(base)
	if err := store.Update([]TaskUpdate{{ID: added[0].ID, Notes: "after publish"}}); err != nil {
		t.Fatalf("Update after published Update: %v", err)
	}
	reloaded := NewTaskStore("/state", "session-1").SetFs(base)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load after published Update: %v", err)
	}
	view := reloaded.View()
	if len(view) != 1 || view[0].Status != TaskDone || len(view[0].Notes) != 1 {
		t.Fatalf("reloaded tasks after published Update = %#v", view)
	}
}

// dirOpenFailFS fails Open for one directory path, leaving every file open and
// the rest of the filesystem intact.
type dirOpenFailFS struct {
	afero.Fs
	dir string
}

func (f *dirOpenFailFS) Open(name string) (afero.File, error) {
	if name == f.dir {
		return nil, errors.New("injected directory open failure")
	}
	return f.Fs.Open(name)
}
