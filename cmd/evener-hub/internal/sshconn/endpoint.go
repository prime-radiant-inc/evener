package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// ErrEndpointMigration marks an installed binary requiring manual migration.
// It is not absence, transport failure, or an AppWire compatibility decision.
var ErrEndpointMigration = errors.New("sshconn: remote hub endpoint migration required")

func endpointMigration(host hostreg.Host) error {
	return fmt.Errorf("%w: host %q config %q requires a manual upgrade before managed endpoint discovery", ErrEndpointMigration, host.Name, host.ConfigPath)
}

func endpointCapability(host hostreg.Host, facts Preflight) error {
	if !facts.LaunchCheckKnown || facts.HubEndpointSchema != hubendpoint.SchemaVersion {
		return endpointMigration(host)
	}
	return nil
}

// resolveHostEndpoint asks the selected remote executable to resolve its own
// config. Addr is bootstrap listener intent, never an inspection override.
// Each call re-reads the running generation, including port-zero assignments.
func (m *Manager) resolveHostEndpoint(ctx context.Context, host hostreg.Host, facts Preflight) (hubendpoint.Report, error) {
	if err := ctx.Err(); err != nil {
		return hubendpoint.Report{}, err
	}
	if facts.ExecutableMissing {
		return hubendpoint.Report{}, ErrExecutableMissing
	}
	if err := endpointCapability(host, facts); err != nil {
		return hubendpoint.Report{}, err
	}
	return m.readHostEndpoint(ctx, host, facts)
}

// readHostEndpoint reads actual remote evidence without inferring capability
// from a launch-check. A proven incompatible live handshake or an already
// recorded deployment repair may use this reader before the replacement's
// contract is verified. Neither authority permits a later unchecked restart.
func (m *Manager) readHostEndpoint(ctx context.Context, host hostreg.Host, facts Preflight) (hubendpoint.Report, error) {
	if err := ctx.Err(); err != nil {
		return hubendpoint.Report{}, err
	}
	if facts.ExecutableMissing {
		return hubendpoint.Report{}, ErrExecutableMissing
	}
	args := []string{"hub", "endpoint", "--json"}
	selector := strings.TrimSpace(host.ConfigPath)
	if selector != "" {
		args = append(args, "--config", selector)
	}
	m.applyResolvedTarget(&host)
	out, err := m.runner.Run(ctx, evenerCommandArgv(m.opts, host, args...), nil)
	if ctx.Err() != nil {
		return hubendpoint.Report{}, ctx.Err()
	}
	if err != nil {
		return hubendpoint.Report{}, sshRunFailure(host.Name, "endpoint inspection", err, tail(out))
	}
	report, err := hubendpoint.DecodeReport(bytes.NewReader(out))
	if err != nil {
		return hubendpoint.Report{}, fmt.Errorf("%w: host %q endpoint report: %w", ErrPreflightDecode, host.Name, err)
	}
	// New reports canonicalize ConfigPath, while old reports and running records
	// can retain invocation spelling. Only absolute paths can be independently
	// compared here. Relative owner paths require checkOwnerConfig's owner cwd
	// and environment, not the SSH login directory or the controller's cwd.
	paths := []string{selector}
	if report.Running != nil && report.Running.ConfigPath != selector {
		paths = append(paths, report.Running.ConfigPath)
	}
	for _, p := range paths {
		if !path.IsAbs(p) || p == report.ConfigPath {
			continue
		}
		matches, err := m.configPathMatches(ctx, host, p, report.ConfigPath, "")
		if ctx.Err() != nil {
			return hubendpoint.Report{}, ctx.Err()
		}
		if err != nil || !matches {
			return hubendpoint.Report{}, fmt.Errorf("%w: host %q endpoint config does not match selected config", ErrPreflightDecode, host.Name)
		}
	}
	return report, nil
}

// canonical is a resolver report, not an invocation spelling. Resolve aliases
// on the remote filesystem without lexical cleaning of symlink/.. segments.
// prefix selects the owner's cwd and narrow environment when that is known.
func (m *Manager) configPathMatches(ctx context.Context, host hostreg.Host, selector, canonical, prefix string) (bool, error) {
	if selector == canonical {
		return true, nil
	}
	out, err := m.ownerRead(ctx, host, prefix+resolveDeployCommand(selector))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == canonical, nil
}
