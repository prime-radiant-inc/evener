package agent

import (
	"fmt"
	"os"
	"syscall"
)

// The watcher runs before TestMain so the test process's HOME, git and temp
// isolation never apply to it, and it exits without running any test.
func init() {
	if os.Getenv(memoryEvalAmbientWatcherEnv) != "1" {
		return
	}
	memoryEvalWatchAmbientReads(os.Args[1:])
	os.Exit(0)
}

// memoryEvalWatchAmbientReads watches each path for IN_OPEN/IN_ACCESS after a
// positive control proves the watch observes a real read of that path. It
// prints READY, waits for stdin to close, and exits 1 when any watched fixture
// file was opened or accessed in between. No mount atime assumption and no
// product hook.
func memoryEvalWatchAmbientReads(paths []string) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(fd)
	buffer := make([]byte, 65536)
	for _, path := range paths {
		if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN|syscall.IN_ACCESS); err != nil {
			panic(err)
		}
		if _, err := os.ReadFile(path); err != nil {
			panic(err)
		}
		n, err := syscall.Read(fd, buffer)
		if err != nil || n == 0 {
			panic("positive inotify read control failed")
		}
	}
	fmt.Println("READY")
	var signal [1]byte
	_, _ = os.Stdin.Read(signal[:])
	n, err := syscall.Read(fd, buffer)
	if err != nil && err != syscall.EAGAIN {
		panic(err)
	}
	if n > 0 {
		fmt.Fprintln(os.Stderr, "ambient fixture file content was opened or accessed")
		os.Exit(1)
	}
}
