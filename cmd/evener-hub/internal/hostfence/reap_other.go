//go:build !linux && !darwin

package hostfence

import (
	"bytes"
	"errors"
	"fmt"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// ReapOptions are the reap's seams. Only Linux and Darwin define a local
// process boundary, so this arm carries none; it keeps the boot seam's call
// site compiling on every platform the hub builds for.
type ReapOptions struct{}

// ErrLocalReapUnsupported reports that this platform has no local process
// boundary to reap through, so an open pending-spawn intent cannot be converged
// here.
var ErrLocalReapUnsupported = errors.New("hostfence: no local process boundary exists on this platform")

// ReapLocalOrphanBoundary is the non-Unix arm of §3's local reap. It cannot
// enumerate a boundary, but it must not skip the durable disposition either: a
// record left `pending` with an open intent is invisible to the admission fence
// and skipped by the interrupted pass, so this arm marks every such record
// `orphan-unverified` with the §9 boundary its own intent data describes. The
// host stays fenced until an operator resolves the record. With no open intents
// it is a no-op.
func ReapLocalOrphanBoundary(store *hostops.Store, opts ReapOptions) (int, error) {
	_ = opts
	if store == nil {
		return 0, nil
	}
	var failures []error
	for _, record := range store.SpawnIntentRecords() {
		if record.State == hostops.StateOrphanUnverified && boundaryHasForeignVariant(record.OrphanBoundary) {
			continue
		}
		if record.State.Terminal() {
			failures = append(failures, fmt.Errorf(
				"record %s is %s with an open spawn intent and cannot be fenced on this platform", record.ID, record.State))
			continue
		}
		if record.State == hostops.StateOrphanUnverified && len(record.PendingSpawns) == 0 {
			continue
		}
		entries, err := boundaryEntries(record.PendingSpawns)
		if err != nil {
			failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
			continue
		}
		if record.State == hostops.StateOrphanUnverified && bytes.Equal(record.OrphanBoundary, entries) {
			// Nothing new to persist: the boundary this platform can describe is
			// already recorded, so a later boot writes nothing.
			continue
		}
		if _, err := store.SetOrphanBoundary(record.ID, entries, nil); err != nil {
			failures = append(failures, fmt.Errorf("record %s: %w", record.ID, err))
			continue
		}
		failures = append(failures, fmt.Errorf("record %s: no local boundary can be enumerated on this platform, so the record stays fenced", record.ID))
	}
	return 0, errors.Join(failures...)
}
