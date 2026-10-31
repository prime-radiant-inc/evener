//go:build unix

package schema

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireSessionLivelock takes the exclusive flock a root session's process
// holds for its lifetime on <dir>/sessions/<id>.livelock, creating the file
// (and the sessions directory) if absent. The returned func releases the
// lock; the OS also releases it if the process dies. A held lock elsewhere
// (a second process on the same session id) surfaces as an error so the
// caller can degrade rather than pretend to hold it.
func AcquireSessionLivelock(dir, id string) (release func(), applied bool, err error) {
	path := SessionLivelockPath(dir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, true, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, true, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, true, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// SessionLivelockHeld probes the session's livelock without taking it:
// held means a live process holds the flock; exists means the file is there
// at all. held=false with exists=true is positive evidence no live process
// holds it (the OS released it at process death); held=false with
// exists=false answers nothing — the session predates livelocks or never
// hosted a process here — and callers must treat that as unknown, never as
// death. The probe's own take-and-release of a free flock is momentary.
func SessionLivelockHeld(dir, id string) (held, exists bool, err error) {
	f, err := os.OpenFile(SessionLivelockPath(dir, id), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer func() { _ = f.Close() }()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, true, nil
	case errorsIsLockHeld(err):
		return true, true, nil
	default:
		return false, true, err
	}
}

// errorsIsLockHeld reports whether a flock error is contention rather than a
// probe failure. EWOULDBLOCK is the non-blocking take's contention answer;
// EINTR would be a retryable interruption, but Go's runtime retries
// interrupted flocks itself, so treating it as contention cannot strand a
// probe on a path that never runs.
func errorsIsLockHeld(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// LivelockContended reports whether an AcquireSessionLivelock error is
// contention with a concurrent holder — a probe's momentary take, a prune
// reclaiming the root's lanes, or a second process on the same session id —
// rather than a real acquisition failure. In this reporting-only primitive
// the mint does not retry or block on it: a session that finds the flock
// held simply runs without one, which costs liveness visibility on ITS
// lanes, never correctness.
func LivelockContended(err error) bool {
	return err != nil && errorsIsLockHeld(err)
}
