//go:build !darwin && !linux

package hub

import (
	"errors"
	"runtime"
)

// hostStateRootFreeSpace reports the free bytes available on the filesystem
// carrying path. This platform has no free-space query wired, so the running
// health predicate's free-space half fails closed: an unanswerable query
// cannot prove the state root is not critically full.
func hostStateRootFreeSpace(path string) (uint64, error) {
	return 0, errors.New("free-space query unavailable on " + runtime.GOOS)
}
