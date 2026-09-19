//go:build linux || darwin

package interactiveartifacts

import (
	"os"
	"syscall"
)

func acquireServiceLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "artifact-owner")
	info, err := f.Stat()
	if err == nil {
		err = requirePrivateInfo(info, false)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
