//go:build unix

package schema

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/afero"
)

// lockSessionMetaCrossProcess takes an exclusive advisory whole-file lock on the
// session's lock file for the duration of a save, serializing the
// load/increment/rename of Revision across processes. The in-process striped
// mutex orders writers within one process, but the daemon rewrites the same
// session's meta out of process (maybeAutoSave), so a second process could
// otherwise read revision N, write N+1, and lose the other's update — leaving
// two rows whose revisions cannot be ordered.
//
// The returned bool reports whether cross-process locking applied: an injected
// non-OS filesystem (tests) has no shared file to lock, so only the in-process
// mutex serializes those writers. The returned func releases the lock. The lock
// file is left in place; flock releases it on close.
func lockSessionMetaCrossProcess(fs afero.Fs, dir, id string) (func(), bool, error) {
	if _, ok := fs.(*afero.OsFs); !ok {
		return func() {}, false, nil
	}
	lockPath := filepath.Join(dir, sessionsSubdir, id+".meta.json.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, true, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, true, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
