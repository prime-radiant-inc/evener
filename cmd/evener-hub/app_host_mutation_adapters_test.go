package hub

import (
	"context"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// writeHubTOMLHosts is an exact write of entries: no preservation of hand-added
// file entries and no machine-managed records.
func writeHubTOMLHosts(path string, entries []hostreg.Host) error {
	return writeHubTOMLHostsRecords(path, entries, nil, hostTOMLRecords{}, false)
}

// Add adapts AddResult for tests: it returns the committed row and reports a
// non-committed arm as the error that arm means, so a test never reads a
// teardown failure or a dropped commit as success. The wire handlers call the
// Result methods directly.
func (m *hubHostManager) Add(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	return committedHostRow(m.AddResult(ctx, params))
}

// committedRow narrows a mutation result to its committed row and reports a
// non-committed arm as the error that arm means. The wire handlers return the
// union; the Add, Update and Remove adapters below use this to hear about a
// teardown failure or a dropped commit instead of reading it as success.
func committedRow(result appwire.HostMutationResult) (appwire.HostRow, error) {
	switch {
	case result.HostMutationCommitted != nil:
		return result.HostMutationCommitted.Host, nil
	case result.HostMutationCommittedRemoved != nil:
		row := result.HostMutationCommittedRemoved.Host
		return appwire.HostRow{
			Name:             row.Name,
			Address:          row.Address,
			User:             row.User,
			KeyPath:          row.KeyPath,
			EvenerPath:       row.EvenerPath,
			ConfigPath:       row.ConfigPath,
			Addr:             row.Addr,
			Roots:            row.Roots,
			Origin:           row.Origin,
			Generation:       row.Generation,
			IncarnationID:    row.IncarnationID,
			Removed:          true,
			RetainedRows:     row.RetainedRows,
			RowsTruncated:    row.RowsTruncated,
			EscalationAgeSec: row.EscalationAgeSec,
			OpenRemnantID:    row.OpenRemnantID,
		}, nil
	case result.HostMutationTeardownFailure != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"the mutation committed but its %s teardown failed; resume it through evener/host/teardown-retry with remnantId %q",
			result.HostMutationTeardownFailure.Seam, result.HostMutationTeardownFailure.RemnantID))
	case result.HostMutationTeardownFailureRemoved != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"the mutation committed but its %s teardown failed; resume it through evener/host/teardown-retry with remnantId %q",
			result.HostMutationTeardownFailureRemoved.Seam, result.HostMutationTeardownFailureRemoved.RemnantID))
	case result.HostMutationCollisionDropped != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"a concurrent hub.toml edit won the race (%s); re-read the host list", result.WinningFingerprint))
	}
	return appwire.HostRow{}, appwire.Conflict("the mutation returned no committed arm")
}

// committedHostRow is committedRow with the caller's error already handled.
func committedHostRow(result appwire.HostMutationResult, err error) (appwire.HostRow, error) {
	if err != nil {
		return appwire.HostRow{}, err
	}
	return committedRow(result)
}

// Update adapts UpdateResult for tests, reporting a non-committed arm as the
// error that arm means.
func (m *hubHostManager) Update(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
	result, err := m.UpdateResult(ctx, params)
	if err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	row, err := committedRow(result)
	if err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	return appwire.HostUpdateResponse{Host: row}, nil
}

// Remove adapts RemoveResult for tests, reporting a non-committed arm as the
// error that arm means.
func (m *hubHostManager) Remove(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
	result, err := m.RemoveResult(ctx, params)
	if err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	row, err := committedRow(result)
	if err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	return appwire.HostRemoveResponse{Host: row}, nil
}
