//go:build darwin

package execenv

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// canonicalPathOfFd hands the kernel the address of a buffer to write the fd's
// path into. If that buffer lives on the goroutine stack and the stack grows
// (and moves) during the call, the kernel writes into the old stack and the
// path reads back empty, so every masked-path recheck on macOS fails closed
// (#3744). Calling it from a fresh goroutine at every stack depth across a
// range makes some call land on a growth boundary inside the fcntl chain.
func TestCanonicalPathOfFdSurvivesStackGrowth(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd) //nolint:errcheck
	for depth := range 300 {
		got := make(chan string, 1)
		go func() { got <- canonicalPathAtDepth(fd, depth) }()
		if path := <-got; path != dir {
			t.Fatalf("at stack depth %d canonicalPathOfFd = %q, want %q", depth, path, dir)
		}
	}
}

// canonicalPathAtDepth calls canonicalPathOfFd beneath depth extra frames, each
// with a little stack of its own.
//
//go:noinline
func canonicalPathAtDepth(fd, depth int) string {
	var pad [64]byte
	if depth > 0 {
		pad[depth%len(pad)] = byte(depth)
		return canonicalPathAtDepth(fd, depth-1) + string(pad[:0])
	}
	path, err := canonicalPathOfFd(fd)
	if err != nil {
		return "error: " + err.Error()
	}
	return path
}
