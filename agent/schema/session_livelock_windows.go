//go:build windows

package schema

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// AcquireSessionLivelock takes the exclusive byte-range lock a root session's
// process holds for its lifetime on <dir>/sessions/<id>.livelock, creating the
// file (and the sessions directory) if absent. The returned func releases the
// lock; the OS also releases it if the process dies. LOCKFILE_FAIL_IMMEDIATELY
// makes contention (a second process on the same session id) surface as an
// error so the caller can degrade rather than pretend to hold it — the same
// non-blocking contract the unix flock arm carries.
func AcquireSessionLivelock(dir, id string) (release func(), applied bool, err error) {
	path := SessionLivelockPath(dir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, true, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, true, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, ^uint32(0), ^uint32(0), &overlapped); err != nil {
		_ = f.Close()
		return nil, true, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
		_ = f.Close()
	}, true, nil
}

// SessionLivelockHeld probes the session's livelock without taking it:
// held means a live process holds the lock; exists means the file is there at
// all. held=false with exists=true is positive evidence no live process holds
// it; held=false with exists=false answers nothing and callers must treat it
// as unknown, never as death.
func SessionLivelockHeld(dir, id string) (held, exists bool, err error) {
	f, err := os.OpenFile(SessionLivelockPath(dir, id), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer func() { _ = f.Close() }()
	var overlapped windows.Overlapped
	lockErr := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, ^uint32(0), ^uint32(0), &overlapped)
	switch {
	case lockErr == nil:
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
		return false, true, nil
	case errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION):
		return true, true, nil
	default:
		return false, true, lockErr
	}
}
