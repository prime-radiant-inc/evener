//go:build !linux && !darwin

package agent

// delegateControllerPlatformExcludedProductionFiles for the remaining GOOS
// (windows and the unshipped others): the root production files they cannot
// load are the unix flock arm and both platform-specific start-time arms
// (linux and darwin). (process_alive_other.go — !unix — and
// process_started_at_other.go — !linux && !darwin — load.)
func delegateControllerPlatformExcludedProductionFiles() map[string]bool {
	return map[string]bool{
		"process_alive_unix.go":        true,
		"process_started_at_darwin.go": true,
		"process_started_at_linux.go":  true,
	}
}
