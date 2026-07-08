//go:build linux

package execenv

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// canonicalRecheckRequired is false on Linux: ext4/xfs/btrfs are case-sensitive
// and RESOLVE_NO_SYMLINKS makes lexical path cleaning match kernel resolution, so
// the pre-open textual denylist check is already authoritative. The fd re-check is
// pure defense-in-depth here and may be skipped if /proc is unavailable.
const canonicalRecheckRequired = false

// canonicalPathOfFd returns the kernel's canonical path for an open fd via
// /proc/self/fd. Used to re-verify the denylist against the real (true-cased)
// path after opening — the seam that closes case/normalization-insensitive
// masking bypasses on other platforms.
func canonicalPathOfFd(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}

// openBeneathRoot opens rel beneath rootFd such that no symlink is traversed and
// the result can never escape rootFd. On Linux this is a single openat2(2) with
// RESOLVE_BENEATH (stay under the anchor) and RESOLVE_NO_SYMLINKS (refuse every
// symlink, including the final component). rel is slash-relative and cleaned
// (never absolute, never containing ".."). flags/mode are the usual open args.
func openBeneathRoot(rootFd int, rel string, flags int, mode uint32) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	}
	return openat2Retry(rootFd, rel, how)
}

// openAbsNoSymlinks opens an absolute path such that no symlink is traversed
// anywhere in it (RESOLVE_NO_SYMLINKS), but without confining to a root — the
// anywhere-minus-denylist read shape. Because no symlink is followed, the cleaned
// textual path is canonical, so the denylist check the caller ran on that text is
// authoritative; a mid-flight symlink swap makes this open fail (ELOOP) rather
// than redirect.
func openAbsNoSymlinks(abs string, flags int, mode uint32) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	}
	return openat2Retry(unix.AT_FDCWD, abs, how)
}

// openRootDir opens an allowed root as a cached anchor fd. O_PATH is enough to
// use it as the dirfd for openat2/openat/renameat/mkdirat/unlinkat/fstatat while
// requiring no read permission on the directory contents.
func openRootDir(path string) (int, error) {
	return unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// openat2Retry retries openat2 across EINTR (and EAGAIN, which openat2 can return
// while the kernel resolves a large path).
func openat2Retry(dirfd int, path string, how *unix.OpenHow) (int, error) {
	for {
		fd, err := unix.Openat2(dirfd, path, how)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		return fd, err
	}
}
