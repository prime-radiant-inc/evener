//go:build !linux && !darwin

package hub

// defaultOrphanVerify is the non-Unix arm of the resolve's enumeration seam:
// this platform has no local process boundary to enumerate, so the record's
// boundary can never be proven clean and stays fenced — the same fail-closed
// disposition hostfence's own non-Unix reap takes.

import (
	"errors"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

func defaultOrphanVerify(hostops.Record) error {
	return errors.New("no local process boundary exists on this platform, so the orphan boundary cannot be enumerated")
}
