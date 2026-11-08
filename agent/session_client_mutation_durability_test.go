package agent

import (
	"errors"
	"os"
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

func TestClientMutationSnapshotSyncsDirectory(t *testing.T) {
	t.Parallel()
	fs := &recordingSyncFS{Fs: afero.NewMemMapFs()}
	snapshot := newEmptyClientMutationSnapshot("session-1")
	renamed, err := saveClientMutationSnapshotFS(fs, "/state", "session-1", snapshot, clientMutationWriteReservation, clientMutationFaults{})
	if err != nil {
		t.Fatalf("saveClientMutationSnapshotFS: %v", err)
	}
	if !renamed {
		t.Fatal("saveClientMutationSnapshotFS reported no rename")
	}
	if !fs.sawSync("/state/mutations") {
		t.Fatalf("client mutation snapshot did not sync its directory; operations: %v", fs.recorded())
	}
}

func TestClientMutationSnapshotDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	fs := &dirOpenFailFS{Fs: base, dir: "/state/mutations"}
	snapshot := newEmptyClientMutationSnapshot("session-1")
	renamed, err := saveClientMutationSnapshotFS(fs, "/state", "session-1", snapshot, clientMutationWriteReservation, clientMutationFaults{})
	if !renamed {
		t.Fatal("directory sync failure lost the rename outcome")
	}
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("saveClientMutationSnapshotFS error = %v, want fsatomic.ErrPublished", err)
	}
	reloaded, loadErr := loadClientMutationSnapshotFS(base, "/state", "session-1")
	if loadErr != nil {
		t.Fatalf("load published snapshot: %v", loadErr)
	}
	if reloaded.SessionID != "session-1" {
		t.Fatalf("published snapshot session = %q", reloaded.SessionID)
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
