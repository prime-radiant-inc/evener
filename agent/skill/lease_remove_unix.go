//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package skill

import "os"

// removeFallbackBase removes a fallback base while its exclusive leases are still
// held, so no reader can acquire one between the check and the removal. The
// leases are released once the directory is gone.
func removeFallbackBase(path string, leases []skillsLease) {
	_ = os.RemoveAll(path)
	for _, lease := range leases {
		_ = lease.Release()
	}
}
