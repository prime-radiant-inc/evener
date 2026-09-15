//go:build unix

package hub

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openEndpointFingerprintKey opens the key file at path for reading without
// following a symlink: O_NOFOLLOW makes a link at the path fail the open, so the
// refusal is atomic with getting the descriptor. The caller judges and reads
// that same descriptor, which is what closes the window a check-then-read-by-
// path left between the two. O_NONBLOCK keeps a FIFO or device at the path from
// blocking the open; the descriptor checks refuse whatever is not the hub's own
// regular file.
func openEndpointFingerprintKey(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open endpoint fingerprint key %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open endpoint fingerprint key %s", path)
	}
	return file, nil
}

// createEndpointFingerprintKey creates the key file at path for writing without
// following a symlink: O_NOFOLLOW keeps a link planted where a fresh key is
// about to be written from redirecting the write, and O_EXCL makes the
// create-if-absent judgement atomic with getting the descriptor. The repair
// writes its temp file through this, so the same discipline the read side keeps
// covers the write side too.
func createEndpointFingerprintKey(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open endpoint fingerprint key %s", path)
	}
	return file, nil
}

// lockEndpointFingerprintKey takes the advisory lock that serializes repairs of
// the key at path across processes: the lock file is path plus ".lock", and the
// flock is blocking, so a process that finds another holding the lock waits for
// it and then judges the file the holder left behind rather than running beside
// it. The open keeps the read side's O_NOFOLLOW discipline - a link planted at
// the lock path must not be able to redirect the lock, and what answers is
// judged a regular file before it is locked - and O_CLOEXEC keeps the
// descriptor out of any child. The caller calls the returned release exactly
// once, when the key path is no longer in use.
func lockEndpointFingerprintKey(path string) (func(), error) {
	lockPath := path + ".lock"
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open endpoint fingerprint key lock %s: %w", lockPath, err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open endpoint fingerprint key lock %s", lockPath)
	}
	closeOnError := func(err error) (func(), error) {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("close endpoint fingerprint key lock %s: %w", lockPath, closeErr))
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("stat endpoint fingerprint key lock %s: %w", lockPath, err))
	}
	if !info.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("%s is not a regular file", lockPath))
	}
	// Blocking, not LOCK_NB: waiting for the holder is the point. The loser of
	// the race has to judge the key the winner published.
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return closeOnError(fmt.Errorf("lock endpoint fingerprint key %s: %w", lockPath, err))
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
