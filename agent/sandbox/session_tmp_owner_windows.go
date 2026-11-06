//go:build windows

package sandbox

import "os"

// SessionTmpSupported reports whether this platform can host a world-usable
// session temp container (session_tmp.go). False here: the container is defined by
// POSIX mode bits (0711 container, 1777 sticky leaf) that Windows does not have,
// and there is no POSIX uid for a child to become, so callers keep the
// pre-existing session-scratch export rather than switching TMPDIR to a container
// that cannot exist.
const SessionTmpSupported = false

// scratchEntryOwnedByProcess reports whether path exists. Windows has no POSIX
// uid to compare: a directory created under the process's token is that user's,
// and the session temp container's permission semantics (0711 container, 1777
// leaf) do not exist there at all. The container's host temp bases are Unix
// paths, so NewSessionTmp finds no base on Windows and the container is never
// created — this merely keeps the package building.
func scratchEntryOwnedByProcess(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		return false, err
	}
	return true, nil
}

// scratchInfoOwnedByProcess is scratchEntryOwnedByProcess over a stat the
// caller already holds; Windows has no POSIX uid to compare, and the caller
// reaching here already holds a successful Lstat, which is the whole of the
// path-based check above.
func scratchInfoOwnedByProcess(info os.FileInfo) bool {
	return info != nil
}
