//go:build unix

package schema

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// AcquireSessionLivelock takes the exclusive flock a root session's process
// holds for its lifetime on <dir>/sessions/<id>.livelock, creating the file
// (and the sessions directory) if absent. The returned func releases the
// lock; the OS also releases it if the process dies. A held lock elsewhere
// (a second process on the same session id) surfaces as an error so the
// caller can degrade rather than pretend to hold it.
//
// Next to the flock the mint also sets the platform's held-marker (Linux's
// OFD lock; Darwin needs none), because flock locks are invisible to the
// non-taking F_GETLK inspection SessionLivelockHeld uses — the hostlock
// marker/query pattern (cmd/evener-hub/internal/hostlock). A probe must never
// be able to delay or deny this registration (review round 6, finding 3): a
// probe that briefly took the flock could make a resume's single non-blocking
// mint fail and leave that session reading dead-looking all its life.
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
	if err := markLivelockHeld(f); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, true, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// SessionLivelockHeld inspects the session's livelock WITHOUT taking it:
// held means a live process holds it; exists means the file is there at all.
// held=false with exists=true is positive evidence no live process holds it
// (the OS released it at process death); held=false with exists=false answers
// nothing — the session predates livelocks or never hosted a process here —
// and callers must treat that as unknown, never as death.
//
// The inspection is F_GETLK, which reports the conflicting lock without
// acquiring anything (the hostlock IsHeld pattern), so a probe can never
// delay or deny a concurrent mint — the take-and-release probe it replaces
// contended with startup's acquisition for the moment it held the flock
// (review round 6, finding 3). On Linux, F_GETLK reports OFD locks and not
// flock locks, so it is the mint's OFD marker the probe sees; Darwin exposes
// flock to GETLK directly. A foreign flock that is not a mint (nothing in
// evener takes one on a livelock file) reads free — the probe answers
// evener's own holding contract, not every lock the file could carry.
func SessionLivelockHeld(dir, id string) (held, exists bool, err error) {
	path := SessionLivelockPath(dir, id)
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false, true, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return false, true, err
	}
	if !os.SameFile(before, opened) {
		return false, true, errors.New("livelock changed while opening: " + path)
	}
	// F_GETLK answers which lock a write request at offset 0 would conflict
	// with, and converts it to F_UNLCK when none would — no lock is taken.
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lock); err != nil {
		return false, true, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return false, true, err
	}
	if !os.SameFile(before, after) {
		return false, true, errors.New("livelock changed during inspection: " + path)
	}
	return lock.Type != unix.F_UNLCK, true, nil
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
// contention with a concurrent holder — a second process on the same session
// id, the one contending holder left now that probes no longer take the
// lock — rather than a real acquisition failure. In this reporting-only
// primitive the mint does not retry or block on it: a session that finds the
// flock held simply runs without one, which costs liveness visibility on ITS
// lanes, never correctness.
func LivelockContended(err error) bool {
	return err != nil && errorsIsLockHeld(err)
}
