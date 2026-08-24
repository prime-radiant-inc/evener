package jobstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestReadOutputWindowSnapshot_NegativeMaxBytes covers the negative
// maxBytes guard (line 94-95).
func TestReadOutputWindowSnapshot_NegativeMaxBytes(t *testing.T) {
	_, err := ReadOutputWindowSnapshot(filepath.Join(t.TempDir(), "x"), 0, -1)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("error = %v, want ErrInvalidLimit", err)
	}
}

// TestReadOutputWindowSnapshot_NegativeOffset covers the negative offset
// guard (line 97-98).
func TestReadOutputWindowSnapshot_NegativeOffset(t *testing.T) {
	_, err := ReadOutputWindowSnapshot(filepath.Join(t.TempDir(), "x"), -1, 10)
	if !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("error = %v, want ErrInvalidOffset", err)
	}
}

// TestReadOutputSnapshotOnce_MissingFile covers the missing-file path
// (line 217-218).
func TestReadOutputSnapshotOnce_MissingFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := readOutputSnapshotOnce(fs, "/nonexistent", 10, false)
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

// TestReadOutputWindowSnapshotOnce_MissingFile covers the missing-file path
// (line 122-123).
func TestReadOutputWindowSnapshotOnce_MissingFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := readOutputWindowSnapshotOnce(fs, "/nonexistent", 0, 10)
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

// TestReadOutputRawSnapshotWindow_OpenError covers the open error path
// (line 189-191).
func TestReadOutputRawSnapshotWindow_OpenError(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := readOutputRawSnapshotWindow(fs, "/nonexistent", 0, 10)
	if err == nil {
		t.Fatal("expected error for opening non-existent file")
	}
}

// TestReadOutputRawSnapshotWindow_ShortRead covers the io.ReadFull error path
// (line 205-206) — when the file has fewer bytes than requested.
func TestReadOutputRawSnapshotWindow_ShortRead(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/test.log"
	afero.WriteFile(fs, path, []byte("short"), 0o644)
	_, err := readOutputRawSnapshotWindow(fs, path, 0, 100)
	if err == nil {
		t.Fatal("expected error for short read")
	}
}

// TestReadOutputSnapshotWindow_OpenError covers the open error path
// in readOutputSnapshotWindow (line 329-330).
func TestReadOutputSnapshotWindow_OpenError(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := readOutputSnapshotWindow(fs, "/nonexistent", 10, 100, false)
	if err == nil {
		t.Fatal("expected error for opening non-existent file")
	}
}

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

// TestReadOutputSnapshotWithRetry_ChangedTwice covers the double-change
// retry path (line 80-82).
func TestReadOutputSnapshotWithRetry_ChangedTwice(t *testing.T) {
	calls := 0
	_, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		calls++
		return OutputSnapshot{}, errOutputChanged
	})
	if !errors.Is(err, ErrOutputChangedDuringRead) {
		t.Fatalf("error = %v, want ErrOutputChangedDuringRead", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestReadOutputWindowSnapshotWithRetry_ChangedTwice covers the double-change
// retry path for window snapshots (line 111-113).
func TestReadOutputWindowSnapshotWithRetry_ChangedTwice(t *testing.T) {
	calls := 0
	_, err := readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
		calls++
		return OutputWindowSnapshot{}, errOutputChanged
	})
	if !errors.Is(err, ErrOutputChangedDuringRead) {
		t.Fatalf("error = %v, want ErrOutputChangedDuringRead", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestReadOutputSnapshotWithRetry_FirstChangedThenOK covers the retry path
// where the first attempt changes but the second succeeds (line 79-83).
func TestReadOutputSnapshotWithRetry_FirstChangedThenOK(t *testing.T) {
	calls := 0
	snap := OutputSnapshot{Content: []byte("ok"), TotalBytes: 2}
	_, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		calls++
		if calls == 1 {
			return OutputSnapshot{}, errOutputChanged
		}
		return snap, nil
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestReadOutputSnapshotWithRetry_FirstOKNoRetry covers the happy path
// (line 75-77) where the first attempt succeeds without a change.
func TestReadOutputSnapshotWithRetry_FirstOKNoRetry(t *testing.T) {
	calls := 0
	snap := OutputSnapshot{Content: []byte("ok"), TotalBytes: 2}
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		calls++
		return snap, nil
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got.TotalBytes != 2 {
		t.Fatalf("TotalBytes = %d, want 2", got.TotalBytes)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestReadOutputWindowSnapshotWithRetry_FirstOKNoRetry covers the happy
// path for window snapshots (line 106-108).
func TestReadOutputWindowSnapshotWithRetry_FirstOKNoRetry(t *testing.T) {
	calls := 0
	snap := OutputWindowSnapshot{Content: []byte("ok"), TotalBytes: 2}
	got, err := readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
		calls++
		return snap, nil
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got.TotalBytes != 2 {
		t.Fatalf("TotalBytes = %d, want 2", got.TotalBytes)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
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
