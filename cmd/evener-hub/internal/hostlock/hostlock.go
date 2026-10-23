// Package hostlock provides an exclusive lock for each selected hub state root.
package hostlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AcquireLock takes exclusive nonblocking ownership on path. Linux retains
// flock for remote PID evidence and adds an OFD marker for non-owning queries.
// The returned release closes the sole descriptor, dropping all ownership.
//
// Two owners of the same state root must never run simultaneously. Separate
// roots also need independently selected rendezvous directories and listeners.
func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir lock parent: %w", err)
	}
	if err := CheckStorage(filepath.Dir(path), nil); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := checkStorageFile(f, path, nil); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := flockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		// Name the path (kata av1j): a raw errno carries none, and the losing
		// operator's remedy — find the holder, or isolate a test hub under its
		// own HOME so lock, run dir, state, and auth token all move together —
		// starts from knowing which lock file this was.
		return nil, fmt.Errorf("flock %s: %w (another evener-hub may already be running; a disposable hub needs its own HOME)", path, err)
	}
	if err := lockMarker(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("hub lock marker %s: %w", path, err)
	}
	return func() {
		_ = f.Close()
	}, nil
}

// IsHeld inspects the existing lock inode without creating or replacing it.
// An absent lock is free; an unsupported inspection is an error, never a guess.
func IsHeld(path string) (bool, error) {
	if !flockInspectionSupported() {
		return false, fmt.Errorf("hub lock inspection unsupported: %s", path)
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !before.Mode().IsRegular() || !lockOwnedByCurrentUser(before) {
		return false, fmt.Errorf("unsafe hub lock: %s", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(before, opened) || !lockOwnedByCurrentUser(opened) {
		return false, fmt.Errorf("hub lock changed while opening: %s", path)
	}
	if err := checkStorageFile(file, path, nil); err != nil {
		return false, err
	}
	held, err := flockIsHeld(file)
	if err != nil {
		return false, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !os.SameFile(before, after) {
		return false, fmt.Errorf("hub lock changed during inspection: %s", path)
	}
	return held, nil
}
