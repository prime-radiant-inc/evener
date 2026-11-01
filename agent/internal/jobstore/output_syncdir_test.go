package jobstore

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/spf13/afero"
)

type dirSyncFS struct {
	afero.Fs
	syncErr error
}

func (fs dirSyncFS) Open(name string) (afero.File, error) {
	f, err := fs.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return dirSyncFile{File: f, syncErr: fs.syncErr}, nil
}

type dirSyncFile struct {
	afero.File
	syncErr error
}

func (f dirSyncFile) Sync() error { return f.syncErr }

// A filesystem that cannot sync a directory at all must not fail the output
// metadata write whose rename already landed; any other sync failure is real.
func TestSyncParentDirToleratesFilesystemsThatCannotSyncDirectories(t *testing.T) {
	mem := afero.NewMemMapFs()
	if err := mem.MkdirAll("/out", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []error{syscall.ENOSYS, syscall.ENOTSUP, syscall.EINVAL} {
		fs := dirSyncFS{Fs: mem, syncErr: &os.PathError{Op: "sync", Path: "/out", Err: unsupported}}
		if err := syncParentDir(fs, "/out/job.log"); err != nil {
			t.Errorf("syncParentDir with %v = %v, want nil", unsupported, err)
		}
	}
	fs := dirSyncFS{Fs: mem, syncErr: &os.PathError{Op: "sync", Path: "/out", Err: syscall.EIO}}
	if err := syncParentDir(fs, "/out/job.log"); !errors.Is(err, syscall.EIO) {
		t.Errorf("syncParentDir with EIO = %v, want it to wrap EIO", err)
	}
}
