//go:build windows

package hub

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// openEndpointFingerprintKey opens the key file at path for reading. O_NOFOLLOW
// is not available on this platform, so the open follows a symlink at the path;
// the checks in readEndpointFingerprintKey judge - and read - what the path
// resolves to rather than the path again, which is what this platform can say
// about it.
func openEndpointFingerprintKey(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}

// createEndpointFingerprintKey creates the key file at path for writing. No
// O_NOFOLLOW exists on this platform, so a link at the path is not refused by
// the open; the repair writes only a temp name it has just generated, and the
// publish step judges the path it replaces (see publishFreshEndpointFingerprintKey).
func createEndpointFingerprintKey(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// lockEndpointFingerprintKey takes the lock that serializes repairs of the key
// at path across processes: the lock file is path plus ".lock", and LockFileEx
// is taken without LOCKFILE_FAIL_IMMEDIATELY, so a process that finds another
// holding the lock waits for it and then judges the file the holder left behind
// rather than running beside it. FILE_FLAG_OPEN_REPARSE_POINT keeps a link
// planted at the lock path from redirecting the lock, and the handle is judged
// to be a regular file, not a directory or another reparse point, before it is
// locked. The caller calls the returned release exactly once, when the key path
// is no longer in use.
func lockEndpointFingerprintKey(path string) (func(), error) {
	lockPath := path + ".lock"
	lockPath16, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("encode endpoint fingerprint key lock path: %w", err)
	}
	handle, err := windows.CreateFile(
		lockPath16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open endpoint fingerprint key lock %s: %w", lockPath, err)
	}
	closeOnError := func(err error) (func(), error) {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("close endpoint fingerprint key lock %s: %w", lockPath, closeErr))
		}
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return closeOnError(fmt.Errorf("stat endpoint fingerprint key lock %s: %w", lockPath, err))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return closeOnError(fmt.Errorf("%s is not a regular file", lockPath))
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		^uint32(0),
		^uint32(0),
		&overlapped,
	); err != nil {
		return closeOnError(fmt.Errorf("lock endpoint fingerprint key %s: %w", lockPath, err))
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, ^uint32(0), ^uint32(0), &overlapped)
		_ = windows.CloseHandle(handle)
	}, nil
}
