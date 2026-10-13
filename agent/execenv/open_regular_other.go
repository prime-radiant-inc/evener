//go:build !unix

package execenv

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
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}

// openRegularFollow is the portable fallback: this platform has no
// O_NONBLOCK to open with, so the open is a plain read open — the
// FIFO-blocking hazard the unix variant guards against is unix-shaped. The
// rest of the contract holds: the descriptor is fstat'd, and only a regular
// file, a directory, or — where allowed — a device reaches the caller.
// The unix variant's streamed flag (a pseudo-rooted regular is a stream
// there) is accepted and ignored: the blocking hazard it guards against is
// unix-shaped too.
func openRegularFollow(path string, admitDevice, streamed bool) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, serr := file.Stat()
	if serr != nil {
		_ = file.Close()
		return nil, serr
	}
	mode := info.Mode()
	admit := mode.IsRegular() || info.IsDir() || (admitDevice && mode&os.ModeDevice != 0)
	if !admit {
		_ = file.Close()
		return nil, fmt.Errorf("open %q: not a regular file", path)
	}
	return file, nil
}
