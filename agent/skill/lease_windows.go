//go:build windows

package skill

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type windowsSkillsLease struct {
	handle     windows.Handle
	overlapped windows.Overlapped
	path       string
}

// windowsLockUnsupported reports that the filesystem does not support locking at
// all, as opposed to another process holding the lock.
func windowsLockUnsupported(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION)
}

// platformAcquireSkillsLease takes a shared (reader) or exclusive (reaper) lock
// on the lock file at path. A lock file that was replaced underneath the lock is
// reported as contended: the lease is on a dead file and guards nothing.
func platformAcquireSkillsLease(path string, exclusive bool) (skillsLease, bool, error) {
	handle, err := openSkillLockFile(path, true)
	if err != nil {
		return nil, false, fmt.Errorf("open skill lease: %w", err)
	}
	closeWith := func(cause error) error {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("close skill lease: %w", closeErr))
		}
		return cause
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, false, closeWith(fmt.Errorf("stat skill lease: %w", err))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, false, closeWith(errors.New("skill lease is not a regular file"))
	}
	lease := &windowsSkillsLease{handle: handle, path: path}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(handle, flags, 0, ^uint32(0), ^uint32(0), &lease.overlapped); err != nil {
		contended := errors.Is(err, windows.ERROR_LOCK_VIOLATION)
		if !contended && windowsLockUnsupported(err) {
			// Locking is unavailable here, so the reaper cannot take an exclusive
			// lease either and never removes anything; the copy is safe unleased
			// rather than a reason to lose every bundled skill.
			return noopSkillsLease{}, false, closeWith(nil)
		}
		return nil, contended, closeWith(fmt.Errorf("lock skill lease: %w", err))
	}
	if !lease.Valid() {
		return nil, true, closeWith(nil)
	}
	return lease, false, nil
}

func (lease *windowsSkillsLease) Valid() bool {
	var locked windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(lease.handle, &locked); err != nil {
		return false
	}
	handle, err := openSkillLockFile(lease.path, false)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var current windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &current); err != nil {
		return false
	}
	return locked.VolumeSerialNumber == current.VolumeSerialNumber &&
		locked.FileIndexHigh == current.FileIndexHigh &&
		locked.FileIndexLow == current.FileIndexLow
}

func (lease *windowsSkillsLease) Release() error {
	unlockErr := windows.UnlockFileEx(lease.handle, 0, ^uint32(0), ^uint32(0), &lease.overlapped)
	closeErr := windows.CloseHandle(lease.handle)
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock skill lease: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close skill lease: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}

// openSkillLockFile opens the lock file, creating it when create is set.
func openSkillLockFile(path string, create bool) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode skill lease path: %w", err)
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	return windows.CreateFile(
		path16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}
