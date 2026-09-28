//go:build linux || darwin

package hub

// defaultLocalOrphanVerify is the local-arm production verification for
// `evener/host/orphan-resolve` (crash-fencing spec 08c §5) on the platforms
// with a local process boundary: the hostfence read-only clean rule with its
// production defaults. A remote-fencing record never reaches it —
// verifyOrphanRecord routes that variant through the helper — and it never
// dials.

import (
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

func defaultLocalOrphanVerify(record hostops.Record) error {
	return hostfence.VerifyOrphanBoundary(record, hostfence.VerifyOptions{})
}
