//go:build linux

package sandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

// privateFile prefers a memfd on Linux: its bytes live only in kernel memory,
// never in a filesystem, and an unnamed regular-file fd is exactly what bwrap's
// --args consumes. A kernel without memfd_create falls back to the portable
// unlinked temp file.
func privateFile(payload []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("evener-child-env", unix.MFD_CLOEXEC)
	if err != nil {
		return unlinkedTempFile(payload)
	}
	f := os.NewFile(uintptr(fd), "evener-child-env")
	if f == nil {
		_ = unix.Close(fd)
		return unlinkedTempFile(payload)
	}
	if err := writeAtStart(f, payload); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
