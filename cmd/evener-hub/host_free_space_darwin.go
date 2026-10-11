//go:build darwin

package hub

import "golang.org/x/sys/unix"

// hostStateRootFreeSpace reports the free bytes available to an unprivileged
// process on the filesystem carrying path. It is the free-space half of
// evener/host/running's health predicate (deploy pipeline 08b §10): the
// unprivileged-available block count (`f_bavail`), never the free-reserved
// count (`f_bfree`). Darwin's statfs has no fragment-size field, and its
// `f_bsize` is the block size the counts are expressed in, so that is the unit
// the available bytes are `f_bavail * f_bsize` there.
func hostStateRootFreeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize == 0 {
		return 0, unix.EINVAL
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
