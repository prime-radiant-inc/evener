//go:build linux || darwin

package hub

// defaultOrphanVerify is the production boundary-enumeration seam for
// `evener/host/orphan-resolve` (crash-fencing spec 08c §5) on the platforms
// with a local process boundary: the hostfence read-only clean rule with its
// production defaults. It never dials — boot and resolve perform no SSH — so a
// remote-fencing record's lease enumeration fails closed until its verifier is
// wired.

import (
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

func defaultOrphanVerify(record hostops.Record) error {
	return hostfence.VerifyOrphanBoundary(record, hostfence.VerifyOptions{})
}
