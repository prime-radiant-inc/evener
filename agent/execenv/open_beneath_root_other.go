//go:build !unix

package execenv

import (
	"fmt"
	"os"
)

// OpenRegularBeneathRoot is the portable fallback: this platform has no openat
// or O_NOFOLLOW, so neither a descriptor-relative walk nor an atomic no-follow
// open is possible. The open is a plain read open (matching
// OpenRegularNoFollow's non-unix variant), and the descriptor is fstat'd
// regular before the caller reads a byte.
//
// Symlink protection here therefore rests on the pre-walk, not on this open:
// the caller's own root validation and the symlinkErrorDeep component walk
// (Lstat before open) are the best available guarantee on this platform. This
// function additionally refuses a root that is a symlink at the moment it
// Lstats it, but the Lstat and the os.Open(path) are separate operations, so a
// root replaced with a symlink in that window is followed. That residual TOCTOU
// cannot be closed without an atomic no-follow open, which this platform does
// not provide; it is documented here rather than hidden, and it is the same
// pre-walk boundary the unix path closes with O_NOFOLLOW on the root open.
func OpenRegularBeneathRoot(path, root string) (*os.File, error) {
	if root != "" {
		if info, lerr := os.Lstat(root); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("open %q: root %q is a symlink, refusing to follow it", path, root)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}
