package sshconn

import (
	"context"
	"fmt"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func (m *Manager) setSelectedRestart(host hostreg.Host, owner selectedOwner, command, version, pin string) pendingRestartState {
	owner = owner.clone()
	pending := pendingRestartState{command: command, selected: &owner, version: version, gitSHA: pin}
	m.mu.Lock()
	m.pendingRestarts[host.Name] = pending
	m.mu.Unlock()
	return pending
}

func (m *Manager) restartSelected(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, version, pin string) error {
	command, ok := owner.supervisor.restartRemote(facts.UID)
	if owner.supervisor.kind != supervisorNone && !ok {
		return fmt.Errorf("%w: selected supervisor cannot be addressed safely", errOwnerEvidence)
	}
	if owner.supervisor.kind == supervisorNone {
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+owner.pid+" -a -d 1,2"), nil)
		if err == nil {
			owner.logPath, _ = parseLogPath(out)
		}
		command = selectedRelaunch(owner)
	}
	// Save before stop, including before a stop/drain timeout or lost SSH reply.
	// Do not save a guessed actual port or a synthesized --addr.
	if _, err := m.revalidateOwnedEndpoint(ctx, host, facts, owner); err != nil {
		return err
	}
	pending := m.setSelectedRestart(host, owner, command, version, pin)
	return m.restartSupervised(ctx, host, facts, pending, true)
}

func selectedRelaunch(owner selectedOwner) string {
	return ownerContext(owner.cwd, owner.env) + relaunchCommand(owner.argv, owner.logPath)
}

func (m *Manager) recoverSelectedRestart(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) error {
	prior := *pending.selected
	m.applyResolvedTarget(&host)
	if !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch {
		return fmt.Errorf("%w: pending host registration changed", errOwnerEvidence)
	}
	report, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return err
	}
	if report.ConfigPath != prior.report.ConfigPath || report.HubStateRoot != prior.report.HubStateRoot {
		return fmt.Errorf("%w: pending config/root changed", errOwnerEvidence)
	}
	if report.State != "stopped" {
		now, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if err != nil {
			return err
		}
		if !sameLaunch(now, prior) {
			return fmt.Errorf("%w: pending owner intent changed", errOwnerEvidence)
		}
		if !sameProcess(now, prior) {
			// A successful replacement may have outlived a lost SSH response. Verify
			// that successor instead of needlessly restarting it again.
			if selectedSuccessor(now, prior, pending.version, pending.gitSHA) {
				m.clearPendingRestart(host.Name)
				return nil
			}
			return fmt.Errorf("%w: pending generation changed without a verified successor", errOwnerEvidence)
		}
		if !sameOwner(now, prior) {
			return fmt.Errorf("%w: pending predecessor changed", errOwnerEvidence)
		}
		return m.restartSupervised(ctx, host, facts, pending, true)
	}
	// Stopped is a resolver-verified free selected lock, not an HTTP timeout.
	// Recheck the service's definition and PID before replay. An ad-hoc replay
	// checks that no newly selected supervisor owns the retained launch.
	if err := m.checkOwnerConfig(ctx, host, prior.argv, prior.cwd, prior.env, report); err != nil {
		return err
	}
	if prior.supervisor.kind != supervisorNone {
		if err := m.revalidateStoppedService(ctx, host, facts, prior); err != nil {
			return err
		}
	} else if facts.OS == "darwin" {
		defs, err := m.launchdOwners(ctx, host, facts)
		if err != nil {
			return err
		}
		for _, def := range defs {
			if err := m.hubExecutableMatches(ctx, host, def.program); err != nil {
				continue
			}
			if err := m.checkOwnerConfig(ctx, host, def.argv, def.cwd, def.env, report); err == nil {
				return fmt.Errorf("%w: stopped launch now supervised", errOwnerEvidence)
			}
		}
	} else {
		sup, err := m.supervisorForOwner(ctx, host, "0", prior.argv, prior.cwd)
		if err != nil {
			return err
		}
		if sup.kind != supervisorNone {
			return fmt.Errorf("%w: stopped launch now supervised", errOwnerEvidence)
		}
	}
	// Re-resolve immediately before start so another selected lock winner cannot
	// be mistaken for the stopped predecessor. No old listener has to disappear.
	latest, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return err
	}
	if latest.State != "stopped" || latest.ConfigPath != report.ConfigPath || latest.HubStateRoot != report.HubStateRoot {
		return fmt.Errorf("%w: stopped selection changed before replay", errOwnerEvidence)
	}
	return m.restartSupervised(ctx, host, facts, pending, false)
}

func (m *Manager) revalidateStoppedService(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner) error {
	if owner.supervisor.kind == supervisorLaunchd {
		return m.revalidateStoppedLaunchd(ctx, host, facts, owner)
	}
	sup, err := m.supervisorForOwner(ctx, host, "0", owner.argv, owner.cwd, owner.supervisor)
	if err != nil {
		return err
	}
	if sup != owner.supervisor {
		return fmt.Errorf("%w: stopped selected service changed", errOwnerEvidence)
	}
	definition, err := m.serviceDefinition(ctx, host, sup)
	if err != nil {
		return err
	}
	if definition != owner.definition {
		return fmt.Errorf("%w: stopped service definition changed", errOwnerEvidence)
	}
	return nil
}

func selectedSuccessor(now, prior selectedOwner, version, pin string) bool {
	running := now.report.Running
	return running != nil && !sameProcess(now, prior) && sameLaunch(now, prior) && running.Version == version && hubBuildSHAMatches(running.BackendGitSHA, pin) && (prior.report.Running == nil || running.BootID != prior.report.Running.BootID)
}

func (m *Manager) waitSelectedSuccessor(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) error {
	var observedVersion, observedSHA string
	for range restartHealthAttempts {
		now, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if now.report.Running != nil {
			observedVersion, observedSHA = now.report.Running.Version, now.report.Running.BackendGitSHA
		}
		if err == nil && selectedSuccessor(now, *pending.selected, pending.version, pending.gitSHA) {
			return nil
		}
		if err := m.opts.waitSleep(ctx, restartHealthInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: selected successor PID, generation, boot or build not verified, version %q, want %q, backend_git_sha %q, want %q", ErrRestart, observedVersion, pending.version, observedSHA, pending.gitSHA)
}
