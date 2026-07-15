//go:build windows

package installid

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type installationIDFileLock struct {
	handle     windows.Handle
	overlapped windows.Overlapped
}

func acquireInstallationIDFileLock(path string) (installationIDLock, bool, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, fmt.Errorf("encode installation ID lock path: %w", err)
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
		return nil, false, fmt.Errorf("open installation ID lock: %w", err)
	}
	closeOnError := func(err error) (installationIDLock, bool, error) {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return nil, false, errors.Join(err, fmt.Errorf("close installation ID lock: %w", closeErr))
		}
		return nil, false, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return closeOnError(fmt.Errorf("stat installation ID lock: %w", err))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return closeOnError(errors.New("installation ID lock is not a regular file"))
	}
	lock := &installationIDFileLock{handle: handle}
	err = windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0),
		^uint32(0),
		&lock.overlapped,
	)
	if err != nil {
		contended := errors.Is(err, windows.ERROR_LOCK_VIOLATION)
		_, _, closeErr := closeOnError(fmt.Errorf("lock installation ID: %w", err))
		return nil, contended && closeErr != nil, closeErr
	}
	return lock, false, nil
}

func (lock *installationIDFileLock) Release() error {
	unlockErr := windows.UnlockFileEx(lock.handle, 0, ^uint32(0), ^uint32(0), &lock.overlapped)
	closeErr := windows.CloseHandle(lock.handle)
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock installation ID: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close installation ID lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
