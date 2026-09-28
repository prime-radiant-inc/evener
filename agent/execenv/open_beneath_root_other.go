//go:build !unix

package execenv

import (
	"fmt"
	"os"
)

// OpenRegularBeneathRoot is the portable fallback: this platform has no
// openat or O_NOFOLLOW, so the descriptor-relative walk is not possible.
// The open is a plain read open (matching OpenRegularNoFollow's non-unix
// variant), and the descriptor is fstat'd regular before the caller reads a
// byte. Intermediate-component symlink protection relies on the pre-walk
// (symlinkErrorDeep) which is the best available guarantee on this platform.
// The root's own final component is still refused when it is a symlink, so a
// caller anchoring at an intermediate directory cannot follow a swapped root.
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
