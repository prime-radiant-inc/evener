//go:build !linux && !darwin

package execenv

import (
	"fmt"
	"os"
)

// OpenRegularNoFollow is the portable fallback: this platform has no
// O_NOFOLLOW, so a leaf symlink is followed there (creating one requires
// elevated privileges on the common non-unix hosts). It keeps the rest of the
// contract: the open never blocks (O_NONBLOCK), and the descriptor is fstat'd
// regular before the caller reads a byte.
func OpenRegularNoFollow(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|os.O_NONBLOCK, 0)
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
