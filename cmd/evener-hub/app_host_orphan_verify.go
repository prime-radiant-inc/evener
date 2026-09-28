package hub

// The resolve's persisted-boundary enumeration (crash-fencing spec 08c §5): a
// remote-fencing record is verified through the pinned helper over the manager's
// one-shot remote runner, every other variant through the platform's local
// process boundary. Both arms are read-only — resolve never signals — and both
// fail closed when they cannot prove the boundary clean.

import (
	"context"
	"fmt"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// verifyOrphanRecord runs the clean-rule check for one record, over the
// caller-injected seam when one is configured (tests, embedders) and the
// production default otherwise.
func (m *hubHostManager) verifyOrphanRecord(ctx context.Context, record hostops.Record) error {
	if m.cfg.orphanVerify != nil {
		return m.cfg.orphanVerify(ctx, record)
	}
	boundary, remote, err := hostfence.DecodeRemoteFencingBoundary(record.OrphanBoundary)
	if err != nil {
		return fmt.Errorf("%w: %w", hostfence.ErrOrphanBoundaryUnenumerable, err)
	}
	if remote {
		var runner hostfence.Runner
		if m.cfg.orphanFenceRunner != nil {
			runner = m.cfg.orphanFenceRunner(record.Host)
		}
		return hostfence.VerifyRemoteFencingEntries(ctx, record.Host, boundary, runner)
	}
	return defaultLocalOrphanVerify(record)
}
