package task

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/execsupport/fsatomic"
)

// recordingSyncFS records the Sync calls made through it, by the name of the
// file or directory handle that was synced.
type recordingSyncFS struct {
	afero.Fs
	mu  sync.Mutex
	ops []string
}

func (r *recordingSyncFS) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *recordingSyncFS) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

func (r *recordingSyncFS) sawSync(name string) bool {
	for _, op := range r.recorded() {
		if op == "sync:"+name {
			return true
		}
	}
	return false
}

func (r *recordingSyncFS) sawSyncUnder(dir string) bool {
	for _, op := range r.recorded() {
		if strings.HasPrefix(op, "sync:"+dir+"/") {
			return true
		}
	}
	return false
}

func (r *recordingSyncFS) Open(name string) (afero.File, error) {
	f, err := r.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, statErr := r.Fs.Stat(name)
	return &recordingSyncFile{File: f, rec: r, isDir: statErr == nil && info.IsDir()}, nil
}

func (r *recordingSyncFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := r.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &recordingSyncFile{File: f, rec: r}, nil
}

func (r *recordingSyncFS) Rename(oldname, newname string) error {
	r.record("rename:" + oldname + "->" + newname)
	return r.Fs.Rename(oldname, newname)
}

type recordingSyncFile struct {
	afero.File
	rec   *recordingSyncFS
	isDir bool
}

func (f *recordingSyncFile) Sync() error {
	f.rec.record("sync:" + f.Name())
	return f.File.Sync()
}

func TestTaskStoreSaveSyncsFileAndDirectory(t *testing.T) {
	t.Parallel()
	fs := &recordingSyncFS{Fs: afero.NewMemMapFs()}
	store := NewTaskStore("/state", "session-1").SetFs(fs)
	if err := store.saveTasks([]Task{{ID: 1, Status: TaskOpen, Description: "task"}}); err != nil {
		t.Fatalf("saveTasks: %v", err)
	}
	tasksDir := "/state/tasks"
	if !fs.sawSyncUnder(tasksDir) {
		t.Fatalf("task save did not sync its temp file; operations: %v", fs.recorded())
	}
	if !fs.sawSync(tasksDir) {
		t.Fatalf("task save did not sync its directory; operations: %v", fs.recorded())
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
