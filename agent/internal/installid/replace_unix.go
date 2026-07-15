//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package installid

import "os"

func atomicReplaceInstallationID(tempPath, destinationPath string, _ bool) error {
	// rename(2) atomically replaces an existing destination on the same
	// filesystem. The temporary file is always created in destinationPath's
	// directory.
	return os.Rename(tempPath, destinationPath)
}
