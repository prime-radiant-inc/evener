package cmdutil

import "os"

// TempDirExists reports whether path names an existing directory, or is empty,
// meaning the platform default. A stale TMPDIR that outlived its directory is
// not one, and os.MkdirTemp with an empty dir fails on it rather than falling
// back to the platform default.
//
// Writability is deliberately not probed: testing it would mean creating and
// removing an entry in the user's temp dir at every process start. Callers
// that can lose a feature rather than fail the process handle that case
// themselves, as the artifact store's fallback and degraded path do.
func TempDirExists(path string) bool {
	if path == "" {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
