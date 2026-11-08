package schema

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	return &recordingSyncFile{File: f, rec: r}, nil
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
	rec *recordingSyncFS
}

func (f *recordingSyncFile) Sync() error {
	f.rec.record("sync:" + f.Name())
	return f.File.Sync()
}

func TestSaveSessionMetaSyncsFileAndDirectory(t *testing.T) {
	t.Parallel()
	fs := &recordingSyncFS{Fs: afero.NewMemMapFs()}
	dir := "/state"
	meta := SessionMeta{
		ID:        "02wMz5TxuyedWwYBSOAa00",
		Name:      "session",
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		UpdatedAt: time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := SaveSessionMetaWithFS(fs, dir, meta); err != nil {
		t.Fatalf("SaveSessionMetaWithFS: %v", err)
	}
	sessionsDir := SessionsDir(dir)
	if !fs.sawSyncUnder(sessionsDir) {
		t.Fatalf("session meta write did not sync its temp file; operations: %v", fs.recorded())
	}
	if !fs.sawSync(sessionsDir) {
		t.Fatalf("session meta write did not sync its directory; operations: %v", fs.recorded())
	}
}

func TestSaveSessionMetaDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	dir := "/state"
	id := "02wMz5TxuyedWwYBSOAa00"
	old := SessionMeta{ID: id, Name: "old", CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}
	if err := SaveSessionMetaWithFS(base, dir, old); err != nil {
		t.Fatalf("seed old meta: %v", err)
	}
	fresh := old
	fresh.Name = "new"
	fresh.UpdatedAt = time.Unix(2, 0).UTC()
	fs := &dirOpenFailFS{Fs: base, dir: SessionsDir(dir)}
	err := SaveSessionMetaWithFS(fs, dir, fresh)
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("SaveSessionMetaWithFS error = %v, want fsatomic.ErrPublished", err)
	}
	got, loadErr := LoadSessionMetaWithFS(base, dir, id)
	if loadErr != nil {
		t.Fatalf("load published meta: %v", loadErr)
	}
	if got.Name != "new" {
		t.Fatalf("published meta name = %q, want new", got.Name)
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
