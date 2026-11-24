//go:build darwin

package agent

// delegateControllerPlatformExcludedProductionFiles for GOOS=darwin: the
// root production files darwin cannot load are the !unix arm, the linux-only
// start-time arm, and the arm for every GOOS that is neither linux nor darwin.
// (process_alive_unix.go and process_started_at_darwin.go load.)
func delegateControllerPlatformExcludedProductionFiles() map[string]bool {
	return map[string]bool{
		"process_alive_other.go":      true,
		"process_started_at_linux.go": true,
		"process_started_at_other.go": true,
	}
}
