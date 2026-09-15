//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package skill

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type unixSkillsLease struct {
	file *os.File
	path string
}

// lockUnsupported reports that the filesystem does not support locking at all,
// as opposed to another process holding the lock.
func lockUnsupported(err error) bool {
	return errors.Is(err, unix.ENOLCK) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

// platformAcquireSkillsLease takes a shared (reader) or exclusive (reaper) flock
// on the lock file at path. A lock file that was replaced underneath the lock is
// reported as contended: the lease is on a dead inode and guards nothing.
func platformAcquireSkillsLease(path string, exclusive bool) (skillsLease, bool, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open skill lease: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeWith := func(cause error) error {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("close skill lease: %w", closeErr))
		}
		return cause
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false, closeWith(fmt.Errorf("stat skill lease: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, false, closeWith(errors.New("skill lease is not a regular file"))
	}
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	if err := unix.Flock(fd, how|unix.LOCK_NB); err != nil {
		contended := errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
		if !contended && lockUnsupported(err) {
			// Locking is unavailable on this filesystem, so the reaper cannot take
			// an exclusive lease here either and never removes anything; the copy
			// is safe unleased rather than a reason to lose every bundled skill.
			return noopSkillsLease{}, false, closeWith(nil)
		}
		return nil, contended, closeWith(fmt.Errorf("lock skill lease: %w", err))
	}
	lease := &unixSkillsLease{file: file, path: path}
	if !lease.Valid() {
		return nil, true, closeWith(nil)
	}
	return lease, false, nil
}

func (lease *unixSkillsLease) Valid() bool {
	var locked unix.Stat_t
	if err := unix.Fstat(int(lease.file.Fd()), &locked); err != nil {
		return false
	}
	var current unix.Stat_t
	if err := unix.Lstat(lease.path, &current); err != nil {
		return false
	}
	return locked.Dev == current.Dev && locked.Ino == current.Ino
}

func (lease *unixSkillsLease) Release() error {
	unlockErr := unix.Flock(int(lease.file.Fd()), unix.LOCK_UN)
	closeErr := lease.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock skill lease: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close skill lease: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
