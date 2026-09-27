//go:build darwin || linux

package hub

import "golang.org/x/sys/unix"

// hostStateRootFreeSpace reports the free bytes available to an unprivileged
// process on the filesystem carrying path. It is the free-space half of
// evener/host/running's health predicate (deploy pipeline 08b §10): the
// unprivileged-available block count (Bavail), never the free-reserved count
// (Bfree), so the floor an operator sets is space this process could really
// write.
func hostStateRootFreeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, unix.EINVAL
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
