package hub

// The gated running probe for evener/host/plan (deploy pipeline 08b §6 step 2,
// §10). The plan's second network round trip runs under the per-host gate, on
// the live attached channel, and presents the durable probe epoch the plan
// persisted first. This file owns the primitive and the production wiring; the
// plan handler owns the order and the refusal mapping.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// probeHostRunning is the gate-aware probe primitive: it runs
// evener/host/running over the host's live channel and answers the plan's
// HostRuntimeProbe or the refusal the plan maps.
//
// It inherits the already-held gate from its caller (plan): it never
// try-acquires the non-reentrant gate a second time. It resolves the channel
// through ChannelIfAttached — never a dial, never the one-shot preflight — and,
// under the held gate, refuses a channel that no longer carries the client the
// plan resolved, so the probe can never pair one generation's facts with
// another's running state. The call is deadline-bounded by the owner-adjustable
// probe timeout, and a hung remote holds no gate past that window.
//
// Errors are classified for §6 step 2: a remote that does not serve the method
// at all — a build predating the handler — is the discriminating
// `handler-absent` arm (PlanHandlerAbsentError), and every other failure is
// `probe-failed`, with a timeout named as such in the prose.
func probeHostRunning(ctx context.Context, manager *sshconn.Manager, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch, timeout time.Duration) (hubcore.HostRuntimeProbe, error) {
	if manager == nil {
		return hubcore.HostRuntimeProbe{}, errors.New("no ssh manager is wired, so the host's live channel cannot be resolved")
	}
	timeout = hostProbeTimeoutFor(timeout)
	channel, ok := manager.ChannelIfAttached(host.Name)
	if !ok {
		return hubcore.HostRuntimeProbe{}, fmt.Errorf("host %q has no live channel; connect it and plan again", host.Name)
	}
	if client == nil || channel.Client() != client {
		// A reconnect can replace the channel between the plan's resolution and
		// this probe; the held gate cannot stop the supervisor's own reconnect
		// from swapping a dropped channel. The probe refuses rather than
		// pairing the resolved generation with a replacement's answer.
		return hubcore.HostRuntimeProbe{}, fmt.Errorf("host %q's attached channel changed generation while the plan held its gate; plan again", host.Name)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var response appwire.HostRunningResponse
	err := client.Request(probeCtx, appwire.MethodEvenerHostRunning, appwire.HostRunningParams{FencingEpoch: epoch}, &response)
	if err != nil {
		if isRunningHandlerAbsent(err) {
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("%w (evener/host/running answered: %w)", PlanHandlerAbsentError{}, err)
		}
		// The caller's own context ending and the probe's deadline are different
		// failures: a canceled caller must not be told the remote timed out.
		switch {
		case errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("probing host %q's running state timed out after %s: %w", host.Name, timeout, err)
		case ctx.Err() != nil:
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("probing host %q's running state was canceled: %w", host.Name, err)
		default:
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("probing host %q's running state failed: %w", host.Name, err)
		}
	}
	probe := hubcore.HostRuntimeProbe{Version: response.BuildRevision, RunningHealthy: response.Healthy}
	if response.ProcessStartTime != "" {
		start, err := time.Parse(time.RFC3339, response.ProcessStartTime)
		if err != nil {
			// The wire promises RFC3339; an unparseable value is not a running
			// state anything can bind, so the probe refuses rather than guessing.
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("host %q's probe carried an unparseable processStartTime %q: %w", host.Name, response.ProcessStartTime, err)
		}
		start = start.UTC()
		probe.ProcessStartTime = &start
	}
	return probe, nil
}

// isRunningHandlerAbsent reports whether err is the remote's "method not found"
// answer to the probe: the one distinct classification §6 step 2 names
// (`handler-absent`, the one-time manual-upgrade migration path), kept apart
// from every other probe failure.
func isRunningHandlerAbsent(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && wire.Code == appwire.CodeMethodNotFound
}

// hostGateFor picks THE per-host gate (deploy pipeline 08b §5): the sshconn
// Manager's own per-host lock when a manager owns the hosts — production, where
// that lock is the gate `Ensure`, the reconnect supervisor, add/update, and
// remove already take — and a standalone in-process gate otherwise (tests,
// embedders), so the pipeline still try-acquires rather than proceeding
// ungated. Either way one hub has exactly one gate per host.
func hostGateFor(manager *sshconn.Manager) hostops.Gate {
	if manager != nil {
		return manager
	}
	return hostops.NewGate()
}

// hostProbeTimeoutFor applies the probe timeout's documented default.
func hostProbeTimeoutFor(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultHostProbeTimeout
	}
	return timeout
}
