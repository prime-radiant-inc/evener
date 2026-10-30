package jobstore

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestObserveOutputSnapshot_StatError covers the stat error path
// (line 276-277) using a failingStatFs.
func TestObserveOutputSnapshot_StatError(t *testing.T) {
	fs := &failingStatFs{base: afero.NewMemMapFs()}
	_, err := observeOutputSnapshot(fs, "/nonexistent")
	if err == nil {
		t.Fatal("expected error for stat failure")
	}
}

// TestChangedFrom covers the outputSnapshotObservation.changedFrom method.
func TestChangedFrom(t *testing.T) {
	// No change.
	before := outputSnapshotObservation{outputObserved: true, outputExists: true, retainedBytes: 10, pendingObserved: true, pendingExists: false, metaObserved: true, metaExists: true, meta: "x"}
	after := before
	if after.changedFrom(before) {
		t.Fatal("expected no change")
	}

	// Output exists changed.
	after.outputExists = false
	if !after.changedFrom(before) {
		t.Fatal("expected change in outputExists")
	}
	after = before

	// Retained bytes changed.
	after.retainedBytes = 20
	if !after.changedFrom(before) {
		t.Fatal("expected change in retainedBytes")
	}
	after = before

	// Pending exists changed.
	after.pendingExists = true
	if !after.changedFrom(before) {
		t.Fatal("expected change in pendingExists")
	}
	after = before

	// Pending content changed.
	after.pending = "new"
	if !after.changedFrom(before) {
		t.Fatal("expected change in pending content")
	}
	after = before

	// Meta exists changed.
	after.metaExists = false
	if !after.changedFrom(before) {
		t.Fatal("expected change in metaExists")
	}
	after = before

	// Meta content changed.
	after.meta = "y"
	if !after.changedFrom(before) {
		t.Fatal("expected change in meta content")
	}
}

// --- Test helpers ---

// failingStatFs wraps an Fs and always returns a non-NotExist error on Stat.
type failingStatFs struct {
	base afero.Fs
}

func (f *failingStatFs) Stat(name string) (os.FileInfo, error) {
	return nil, errors.New("stat failed")
}

// Delegate all other afero.Fs methods to the base.
func (f *failingStatFs) Name() string                              { return f.base.Name() }
func (f *failingStatFs) Create(name string) (afero.File, error)    { return f.base.Create(name) }
func (f *failingStatFs) Mkdir(name string, perm os.FileMode) error { return f.base.Mkdir(name, perm) }
func (f *failingStatFs) MkdirAll(path string, perm os.FileMode) error {
	return f.base.MkdirAll(path, perm)
}
func (f *failingStatFs) Open(name string) (afero.File, error) { return f.base.Open(name) }
func (f *failingStatFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	return f.base.OpenFile(name, flag, perm)
}
func (f *failingStatFs) Remove(name string) error                  { return f.base.Remove(name) }
func (f *failingStatFs) RemoveAll(path string) error               { return f.base.RemoveAll(path) }
func (f *failingStatFs) Rename(oldname, newname string) error      { return f.base.Rename(oldname, newname) }
func (f *failingStatFs) Chmod(name string, mode os.FileMode) error { return f.base.Chmod(name, mode) }
func (f *failingStatFs) Chown(name string, uid, gid int) error     { return f.base.Chown(name, uid, gid) }
func (f *failingStatFs) Chtimes(name string, atime, mtime time.Time) error {
	return f.base.Chtimes(name, atime, mtime)
}
