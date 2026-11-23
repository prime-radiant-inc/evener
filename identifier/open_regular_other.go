//go:build !unix

package identifier

import (
	"fmt"
	"os"
)

// OpenRegularNoFollow is the portable fallback: this platform has no
// O_NOFOLLOW, so a leaf symlink is followed there (creating one requires
// elevated privileges on the common non-unix hosts), and the open is a plain
// read open — the FIFO-blocking hazard the unix variant guards against is
// unix-shaped. The rest of the contract holds: the descriptor is fstat'd
// regular before the caller reads a byte.
//
// It is the one implementation of that guard, shared with execenv's readers of
// workspace-controlled paths, so the regular-file requirement cannot drift
// apart between packages.
func OpenRegularNoFollow(path string) (*os.File, error) {
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
		return nil, fmt.Errorf("open %q: %w", path, ErrNotRegularFile)
	}
	return file, nil
}
