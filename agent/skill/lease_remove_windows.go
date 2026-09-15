//go:build windows

package skill

import "os"

// removeFallbackBase removes a fallback base after closing its leases: Windows
// cannot remove a directory entry while a handle to a file inside it is open,
// even with FILE_SHARE_DELETE. A fallback base belongs to one process, whose own
// base the caller skips, so nothing legitimate can acquire a lease in the gap.
func removeFallbackBase(path string, leases []skillsLease) {
	for _, lease := range leases {
		_ = lease.Release()
	}
	_ = os.RemoveAll(path)
}
