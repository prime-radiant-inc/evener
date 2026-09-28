//go:build linux

package hub

import "golang.org/x/sys/unix"

// hostStateRootFreeSpace reports the free bytes available to an unprivileged
// process on the filesystem carrying path. It is the free-space half of
// evener/host/running's health predicate (deploy pipeline 08b §10): the
// unprivileged-available block count (`f_bavail`), never the free-reserved
// count (`f_bfree`), scaled by the *fragment* size — POSIX statvfs defines the
// available bytes as `f_bavail * f_frsize`, and Linux's `f_bsize` is the
// optimal transfer size, which diverges from `f_frsize` on some FUSE/NFS/CIFS
// mounts. Using `f_bsize` there would overestimate free space and could report
// a root below the owner-set floor as healthy.
func hostStateRootFreeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Frsize <= 0 {
		return 0, unix.EINVAL
	}
	return stat.Bavail * uint64(stat.Frsize), nil
}
