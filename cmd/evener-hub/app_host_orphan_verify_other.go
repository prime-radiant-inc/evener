//go:build !linux && !darwin

package hub

// defaultLocalOrphanVerify is the non-Unix arm of the resolve's local
// enumeration: this platform has no local process boundary to enumerate, so a
// local record can never be proven clean and stays fenced — the same
// fail-closed disposition hostfence's own non-Unix reap takes. A remote-fencing
// record never reaches it; verifyOrphanRecord routes that variant through the
// helper.

import (
	"errors"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

func defaultLocalOrphanVerify(hostops.Record) error {
	return errors.New("no local process boundary exists on this platform, so the orphan boundary cannot be enumerated")
}
