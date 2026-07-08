//go:build darwin

package execenv

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// canonicalRecheckRequired is true on macOS: the default APFS volume is
// case-insensitive and unicode-normalization-insensitive, so the kernel opens
// ".SSH" as the real ".ssh" while the pre-open textual denylist check (byte-exact)
// would miss it. The fd re-check on the kernel's true-cased path is therefore
// mandatory here; if it cannot be performed, the operation fails closed.
const canonicalRecheckRequired = true

// canonicalPathOfFd returns the kernel's canonical path for an open fd via
// fcntl(F_GETPATH), which reports the path with its real on-disk casing and
// normalization — defeating case/normalization-insensitive masking bypasses.
func canonicalPathOfFd(fd int) (string, error) {
	var buf [unix.PathMax]byte
	if _, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return "", errno
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

// openBeneathRoot opens rel beneath rootFd refusing every symlink and any escape
// above rootFd. macOS has no openat2, so this walks rel one component at a time
// with openat(O_NOFOLLOW): a symlinked component fails with ELOOP instead of being
// followed, and each open is relative to the previous component's fd so resolution
// stays anchored beneath rootFd. ".." is rejected outright (the RESOLVE_BENEATH
// analogue); callers only ever pass cleaned, "..-free" relative paths.
func openBeneathRoot(rootFd int, rel string, flags int, mode uint32) (int, error) {
	return walkComponents(rootFd, relComponents(rel), flags, mode)
}

// openAbsNoSymlinks opens an absolute path refusing every symlink, without
// confining to a root — the anywhere-minus-denylist read shape. It walks from "/"
// with the same O_NOFOLLOW component discipline, so the opened fd corresponds
// exactly to the cleaned textual path the caller already denylist-checked.
func openAbsNoSymlinks(abs string, flags int, mode uint32) (int, error) {
	rootFd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(rootFd)
	// abs is cleaned and absolute; its components are the path minus the leading "/".
	return walkComponents(rootFd, relComponents(abs[1:]), flags, mode)
}

// openRootDir opens an allowed root as a cached anchor fd (macOS has no O_PATH, so
// a plain O_DIRECTORY handle serves as the dirfd for the openat walk).
func openRootDir(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// walkComponents opens comps one at a time beneath startFd (which it does NOT
// close), applying O_NOFOLLOW at every step so no symlink is ever traversed. The
// final component is opened with the caller's flags; intermediates are opened as
// directories. A ".." component is rejected as an escape.
func walkComponents(startFd int, comps []string, flags int, mode uint32) (int, error) {
	if len(comps) == 0 {
		return unix.Openat(startFd, ".", flags|unix.O_CLOEXEC, mode)
	}
	cur := startFd
	curOwned := false
	closeCur := func() {
		if curOwned {
			_ = unix.Close(cur)
		}
	}
	for i, comp := range comps {
		if comp == ".." {
			closeCur()
			return -1, errEscapesRoot
		}
		last := i == len(comps)-1
		var (
			next int
			err  error
		)
		if last {
			next, err = unix.Openat(cur, comp, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		} else {
			next, err = unix.Openat(cur, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			// A symlinked component (ELOOP, or ENOTDIR with O_DIRECTORY) is
			// classified into the shared symlink sentinel for a legible denial.
			if isSymlinkAt(cur, comp) {
				err = errSymlinkComponent
			}
			closeCur()
			return -1, err
		}
		closeCur()
		cur, curOwned = next, true
	}
	return cur, nil
}
