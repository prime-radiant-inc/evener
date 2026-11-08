//go:build linux

package sandbox

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// diskSpelling returns the existing directory p as the filesystem stores it,
// walking p one component at a time from / without following any symlink: a
// path that runs through a link is not the directory it names, so a link
// planted at a reported path cannot pass for the directory it points to.
//
// The spelling comes from /proc/self/fd, which reports the opened directory's
// path with the filesystem's own letter case and Unicode normalization — the
// same guarantee fcntl(F_GETPATH) gives the macOS implementation. A
// case-insensitive Linux volume (ext4 casefold, vfat) resolves "CASEHOME" to
// the stored "casehome", and only this lookup reports the stored name.
func diskSpelling(p string) (string, bool) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", false
	}
	for comp := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
		if comp == "" {
			continue
		}
		next, err := unix.Openat(fd, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return "", false
		}
		fd = next
	}
	defer func() { _ = unix.Close(fd) }()
	s, err := FdPath(fd)
	return s, err == nil && s != ""
}
