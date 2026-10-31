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

// SessionLivelockHeld reports that this platform has no lock inspection that
// does not first take the lock: Windows offers no F_GETLK equivalent, and a
// take-and-release probe would contend with the mint's single non-blocking
// acquisition — able to make a resume's registration fail and its lanes read
// dead-looking all its life (review round 6, finding 3). An unsupported
// inspection is an error, never a guess, exactly as hostlock answers on the
// platforms it cannot inspect: callers must treat it as unknown.
func SessionLivelockHeld(_, _ string) (held, exists bool, err error) {
	return false, false, errors.New("session livelock inspection is unsupported on this platform")
}
