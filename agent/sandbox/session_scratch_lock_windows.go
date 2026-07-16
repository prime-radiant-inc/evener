//go:build windows

package sandbox

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type windowsScratchLease struct {
	handle     windows.Handle
	overlapped windows.Overlapped
}

func acquireScratchLease(path string) (scratchLease, bool, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, fmt.Errorf("encode lease path: %w", err)
	}
	handle, err := windows.CreateFile(
		path16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, false, fmt.Errorf("open lease: %w", err)
	}
	closeWith := func(cause error) error {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("close lease: %w", closeErr))
		}
		return cause
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, false, closeWith(fmt.Errorf("stat lease: %w", err))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, false, closeWith(errors.New("lease is not a regular file"))
	}
	lease := &windowsScratchLease{handle: handle}
	err = windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0),
		^uint32(0),
		&lease.overlapped,
	)
	if err != nil {
		contended := errors.Is(err, windows.ERROR_LOCK_VIOLATION)
		return nil, contended, closeWith(fmt.Errorf("lock lease: %w", err))
	}
	return lease, false, nil
}

func (lease *windowsScratchLease) Release() error {
	unlockErr := windows.UnlockFileEx(lease.handle, 0, ^uint32(0), ^uint32(0), &lease.overlapped)
	closeErr := windows.CloseHandle(lease.handle)
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock lease: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close lease: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
