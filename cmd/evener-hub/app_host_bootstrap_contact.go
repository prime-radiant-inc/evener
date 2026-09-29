package hub

// This file owns the production first-contact caller crash-fencing §6 names:
// the attach/Ensure wiring that drives `hostfence.Bootstrap` for a host at first
// contact. The flow itself (the exemption, the attempt fence, the claim gate,
// the delivery, the finalize, and the recovery path) is the hostfence package's;
// this caller supplies the attempt's expected identity from the held registry
// row, the operation-store evidence the exemption reads, the durable epoch the
// attempt runs under, and the remote seam — and maps every refusal onto the
// attach surface's typed classes.
//
// The one trigger is the first-attach repair (§6:131: "the first-attach repair
// for starting a stopped hub"), reached from sshconn's bootstrapHub through
// Manager.SetBootstrapHook. Nothing here auto-installs out of band, migrates in
// band, or falls back to an ordinary-SSH claim-then-check: without the
// host-side claim-plus-quiesce primitive delivery stays unavailable (§6:135)
// and the flow refuses fail-closed with the typed `fencing-helper-absent`,
// which is §6:137/:141's out-of-band provisioning posture.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// BootstrapFirstContact drives crash-fencing §6's first-contact flow for one
// host the attach ladder is about to start. It satisfies sshconn's
// BootstrapHook and is wired only there (newHubHostManager), so the one exempt
// delivery step has exactly one production trigger: the first-attach repair,
// before its launch's first mutating remote command.
//
// The attempt is bound to the registration the ladder resolved — the held
// registry row's (generation, incarnation id, presence epoch) triple. Every
// fence, revalidation, and finalize write verifies the live entry still carries
// it, so an attempt that pauses across a remove and re-add refuses typed with
// `stale-entry` without touching the new incarnation. A host already carrying
// the converged `helperInstalled` record is the common case after the first
// attach: it returns immediately, with no attempt fence, no epoch, and no
// remote step.
func (m *hubHostManager) BootstrapFirstContact(ctx context.Context, host hostreg.Host) error {
	name := strings.TrimSpace(host.Name)
	if name == "" {
		return errors.New("the first-contact bootstrap needs a host name")
	}
	identity := hostfence.BootstrapIdentity{
		Generation: host.Generation, IncarnationID: host.IncarnationID, PresenceEpoch: host.PresenceEpoch,
	}
	if identity.IsZero() {
		return fmt.Errorf("host %q carries no registration, so no first-contact attempt can be bound to it", name)
	}
	store := m.bootstrapStore()
	// The identity-bound read (§6:133's binding): a row the ladder resolved
	// before a remove and re-add refuses here, before any attempt fence, claim,
	// or probe exists to touch.
	record, err := store.Provisioning(name, identity)
	if err != nil {
		return bootstrapRefusal(name, err)
	}
	if record.Provisioned() {
		return nil
	}
	// §6:131: the exempt step "runs under the worker's persisted epoch". The
	// first-attach repair has no deploy/restart worker, so the attempt's epoch
	// is minted through the hub's one durable per-host epoch writer — the
	// allocator plan/deploy/restart persist through, whose per-host sequence is
	// never reused — and §6:133's fence write records it in hub.toml before the
	// attempt's first remote side effect, which is what §6:139's recovery names
	// the crashed attempt by.
	attempt, err := m.persistProbeEpoch(host)
	if err != nil {
		return fmt.Errorf("host %q: no durable epoch could be persisted for the first-contact attempt, so nothing was attempted: %w", name, err)
	}
	outcome, err := hostfence.Bootstrap(ctx, hostfence.BootstrapRequest{
		Host:     name,
		Identity: identity,
		Epoch:    hostfence.Epoch{BootID: attempt.BootID, OpSeq: attempt.OpSeq},
		Evidence: m.bootstrapEvidence(name),
		Store:    store,
		// Runner is the manager's ssh process seam. The claim primitive and the
		// recovery probe are deliberately nil in production: no pre-existing
		// trusted host-side primitive and no host-side attempt probe exist yet,
		// so the flow refuses fail-closed instead of delivering or opening the
		// fenced path unverified (§6:135/:137/:139).
		Runner:  m.bootstrapRunnerFor(name),
		Quiesce: m.bootstrapClaimPrimitive(host),
		Probe:   m.bootstrapRecoveryProbe(host),
		Logf:    m.logf,
	})
	if err != nil {
		if ctx.Err() != nil {
			// The caller's own context ended: that is not the flow's refusal and
			// nothing about the host is known, so the raw error stays.
			return err
		}
		return bootstrapRefusal(name, err)
	}
	if outcome.ReleaseErr != nil {
		// The host-side claim may still be held: never report the attempt clean.
		return bootstrapRefusal(name, outcome.ReleaseErr)
	}
	return nil
}

// bootstrapEvidence computes §6:131's operation-store half of the exactly-once
// exemption: a prior fenced epoch (an operation record that persisted a fencing
// epoch for this host) and an interrupted record from a crashed incarnation.
// Both mean prior remote work may still run, so the exemption never opens
// again. The sidecar half (the attempt fence, `helperInstalled`, and the
// version record) is the flow's own read.
func (m *hubHostManager) bootstrapEvidence(name string) hostfence.BootstrapEvidence {
	evidence := hostfence.BootstrapEvidence{}
	if m.cfg.ops == nil {
		return evidence
	}
	for _, record := range m.cfg.ops.Records() {
		if record.Host != name {
			continue
		}
		if len(record.FencingEpoch) > 0 {
			evidence.FencedEpoch = true
		}
		if record.State == hostops.StateInterrupted {
			evidence.Interrupted = true
		}
	}
	return evidence
}

// bootstrapRunnerFor returns the delivery/self-test runner for host, or nil
// when no manager-backed seam is wired (a hub with no ssh process seam fails
// closed in the flow).
func (m *hubHostManager) bootstrapRunnerFor(name string) hostfence.Runner {
	if m.cfg.bootstrapRunner == nil {
		return nil
	}
	return m.cfg.bootstrapRunner(name)
}

// bootstrapClaimPrimitive returns §6:135's host-side atomic claim-plus-quiesce
// primitive, or nil when none is wired. Nil is the honest production value
// today: no pre-existing trusted host-side primitive exists in this build, so
// delivery stays unavailable and the flow refuses typed (§6:137/:141) rather
// than degrading to an ordinary-SSH claim-then-check the spec forbids.
func (m *hubHostManager) bootstrapClaimPrimitive(entry hostreg.Host) hostfence.ClaimQuiesce {
	if m.cfg.bootstrapQuiesce == nil {
		return nil
	}
	return m.cfg.bootstrapQuiesce(entry)
}

// bootstrapRecoveryProbe returns §6:139's read-only recovery re-probe, or nil
// when none is wired. Nil leaves a crashed attempt unverifiable, which the flow
// refuses fail-closed as §6's absent class.
func (m *hubHostManager) bootstrapRecoveryProbe(entry hostreg.Host) hostfence.AttemptProbe {
	if m.cfg.bootstrapProbe == nil {
		return nil
	}
	return m.cfg.bootstrapProbe(entry)
}

// bootstrapRefusal maps one first-contact failure onto the attach surface's
// typed refusals. The helper-gate classes (§8:161), the stale-registration
// class, and the live crashed-attempt class ride the shared fencing conversion;
// everything else is an internal failure — never `probe-failed`, which names
// only the deploy-step re-probe read, so a client can never mistake a bootstrap
// failure for a retryable probe.
func bootstrapRefusal(name string, err error) error {
	if wire, ok := fencingRefusalWire(name, err); ok {
		return wire
	}
	return appwire.InternalError(fmt.Sprintf("host %q: the first-contact bootstrap failed: %v", name, err))
}
