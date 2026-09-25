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
func OpenRegularBeneathRoot(path, root string) (*os.File, error) {
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
