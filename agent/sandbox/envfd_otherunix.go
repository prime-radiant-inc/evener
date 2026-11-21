//go:build unix && !linux

package sandbox

import "os"

// privateFile on the non-Linux Unix hosts (macOS) uses an unlinked temp file:
// the payload fd is inherited by sandbox-exec and read by the child-env reader,
// and no name is left behind for another process to open.
func privateFile(payload []byte) (*os.File, error) {
	return unlinkedTempFile(payload)
}
