//go:build aix

package installid

import "os"

func atomicReplaceInstallationID(tempPath, destinationPath string, _ bool) error {
	return os.Rename(tempPath, destinationPath)
}
