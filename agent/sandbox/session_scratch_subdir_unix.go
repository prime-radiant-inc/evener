//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// scratchModesRecorded reports whether this platform records POSIX permission and
// sticky bits that a mode-based safety judgement can read. It is a var so tests can
// exercise the other platform's behaviour.
var scratchModesRecorded = true

// linkCount returns the number of hard links to the file info describes, which is
// what decides whether a mode change would reach outside the scratch.
func linkCount(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// Nlink's width is platform-dependent (uint16 on darwin, uint64 on linux), so
	// the field is narrowed to int once here and the callers never see the width.
	return int(stat.Nlink), true
}

// fileOwnerID returns the uid that owns the described file, which decides whether a
// sticky, other-writable directory is one this process may treat as unwriteable by
// others: the directory's own owner can remove anything inside it, sticky bit or not. It
// is a var so tests can exercise a foreign owner without another user's account.
var fileOwnerID = func(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

// currentUserID is the uid this process runs as, for comparing against a sticky
// directory's owner.
func currentUserID() int { return os.Getuid() }

// openScratchDirNoFollow opens path as a directory without following a symlink
// at its final component. O_NOFOLLOW makes the kernel refuse a symlink with
// ELOOP, and O_DIRECTORY refuses a regular file, so a scratch subdirectory
// planted as a symlink can never be opened — and the fchmod that
// ensureScratchSubdir performs through this handle therefore cannot be
// redirected outside the scratch.
func openScratchDirNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open scratch subdirectory %q: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// lockScratchDirFile takes a blocking advisory lock on an open directory handle: the
// exclusive form serializes Evener's own writes inside one scratch (a setup window, a
// layout repair), and the shared form counts the consumers sharing it. flock is per
// open file description, so two handles on the same directory contend even inside one
// process, and every holder releases when the handle closes.
func lockScratchDirFile(file *os.File, shared bool) error {
	how := unix.LOCK_EX
	if shared {
		how = unix.LOCK_SH
	}
	if err := unix.Flock(int(file.Fd()), how); err != nil {
		return fmt.Errorf("sandbox: lock scratch directory %q: %w", file.Name(), err)
	}
	return nil
}

// tryLockScratchDirFile takes the exclusive lock without waiting. It reports false
// when another holder remains, which is what makes "am I the last consumer?" a
// question the kernel answers atomically.
func tryLockScratchDirFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, fmt.Errorf("sandbox: probe scratch directory %q lock: %w", file.Name(), err)
}

// unlockScratchDirFile releases the advisory lock the handle holds. Closing the handle
// releases it too, so this is only needed while the handle stays open.
func unlockScratchDirFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("sandbox: unlock scratch directory %q: %w", file.Name(), err)
	}
	return nil
}
