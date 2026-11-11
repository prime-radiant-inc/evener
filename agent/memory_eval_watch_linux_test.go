package agent

import (
	"fmt"
	"os"
	"syscall"
)

// The watcher exits from init, before TestMain and without running any test,
// so TestMain's HOME, git and temp isolation never apply to it. Package-level
// variable initializers, including the offline toolchain probe, still run
// first; the child's environment has no HOME or XDG variables, so they cannot
// reach the watched fixture files.
func init() {
	if os.Getenv(memoryEvalAmbientWatcherEnv) != "1" {
		return
	}
	os.Exit(memoryEvalWatchAmbientReads(os.Args[1:]))
}

// memoryEvalWatchAmbientReads watches each path for IN_OPEN/IN_ACCESS after a
// positive control proves the watch observes a real read of that path. It
// prints READY, waits for stdin to close, and returns exit status 1 when any
// watched fixture file was opened or accessed in between. No mount atime
// assumption and no product hook.
func memoryEvalWatchAmbientReads(paths []string) int {
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
		return 1
	}
	return 0
}
