//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package skill

import "os"

// removeFallbackBase removes a fallback base while its (no-op) leases are held,
// then releases them.
func removeFallbackBase(path string, leases []skillsLease) {
	_ = os.RemoveAll(path)
	for _, lease := range leases {
		_ = lease.Release()
	}
}
