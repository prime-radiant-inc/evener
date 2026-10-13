//go:build !unix

package execenv

import "os"

// listDirOpenDir is the portable stand-in for the O_DIRECTORY fence: a plain
// os.Open, because named-pipe opens fail promptly on the platforms without
// the flag, so there is nothing to fence against.
func listDirOpenDir(absPath string) (*os.File, error) {
	return os.Open(absPath)
}
