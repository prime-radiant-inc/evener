//go:build linux

package agent

// delegateControllerPlatformExcludedProductionFiles for GOOS=linux: the root
// production files linux cannot load are the !unix and !linux arms.
// (process_alive_unix.go and process_started_at_linux.go load.)
func delegateControllerPlatformExcludedProductionFiles() map[string]bool {
	return map[string]bool{
		"process_alive_other.go":      true,
		"process_started_at_other.go": true,
	}
}
