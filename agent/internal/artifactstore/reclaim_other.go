//go:build !unix

package artifactstore

import "os"

// deadStoreDir cannot prove ownership on this platform, so startup reclamation
// leaves every directory in place.
func deadStoreDir(string, os.FileInfo) bool {
	return false
}
