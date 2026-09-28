//go:build !linux && !darwin

package hostfence

import (
	"errors"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// ReapOptions are the reap's seams. Only Linux and Darwin define a local
// process boundary, so this arm carries none; it keeps the boot seam's call
// site compiling on every platform the hub builds for.
type ReapOptions struct{}

// ErrLocalReapUnsupported reports that this platform has no local process
// boundary to reap through, so an open pending-spawn intent cannot be
// converged here.
var ErrLocalReapUnsupported = errors.New("hostfence: no local process boundary exists on this platform")

// ReapLocalOrphanBoundary is the non-Unix arm of §3's local reap. With no open
// intents it is a no-op; with one it reports the fence rather than dropping it,
// because it has no boundary to enumerate.
func ReapLocalOrphanBoundary(store *hostops.Store, opts ReapOptions) (int, error) {
	_ = opts
	if store == nil {
		return 0, nil
	}
	if len(store.SpawnIntentRecords()) > 0 {
		return 0, ErrLocalReapUnsupported
	}
	return 0, nil
}
