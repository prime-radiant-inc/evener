package sshconn

import (
	"context"
	"fmt"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

func (m *Manager) setSelectedRestart(host hostreg.Host, owner selectedOwner, command, version, pin string) pendingRestartState {
	owner = owner.clone()
	previous := m.pendingRestart(host.Name)
	pending := pendingRestartState{binding: previous.binding, replaced: previous.replaced, command: command, selected: &owner, expectedVersion: version, expectedGitSHA: pin}
	m.mu.Lock()
	m.pendingRestarts[host.Name] = pending
	m.mu.Unlock()
	return pending
}

func (m *Manager) prepareSelectedRestart(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, version, pin string) (pendingRestartState, error) {
	return m.prepareSelectedRestartUsing(ctx, host, facts, owner, version, pin, m.resolveHostEndpoint)
}

func (m *Manager) prepareSelectedRestartUsing(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, version, pin string, read func(context.Context, hostreg.Host, Preflight) (hubendpoint.Report, error)) (pendingRestartState, error) {
	pending, err := m.selectedRestartIntentUsing(ctx, host, facts, owner, version, pin, read)
	if err != nil {
		return pendingRestartState{}, err
	}
	return m.setSelectedRestart(host, *pending.selected, pending.command, version, pin), nil
}

func (m *Manager) selectedRestartIntentUsing(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, version, pin string, read func(context.Context, hostreg.Host, Preflight) (hubendpoint.Report, error)) (pendingRestartState, error) {
	command, ok := owner.supervisor.restartRemote(facts.UID)
	if owner.supervisor.kind != supervisorNone && !ok {
		return pendingRestartState{}, fmt.Errorf("%w: selected supervisor cannot be addressed safely", errOwnerEvidence)
	}
	if owner.supervisor.kind == supervisorNone && owner.pid != "0" {
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+owner.pid+" -a -d 1,2"), nil)
		if err == nil {
			owner.logPath, _ = parseLogPath(out)
		}
		command = selectedRelaunch(owner)
	}
	// Save before stop, including before a stop/drain timeout or lost SSH reply.
	// Do not save a guessed actual port or a synthesized --addr.
	if _, err := m.revalidateOwnedEndpointUsing(ctx, host, facts, owner, read); err != nil {
		return pendingRestartState{}, err
	}
	owner = owner.clone()
	return pendingRestartState{command: command, selected: &owner, expectedVersion: version, expectedGitSHA: pin}, nil
}

func selectedRelaunch(owner selectedOwner) string {
	return ownerContext(owner.cwd, owner.env) + relaunchCommand(owner.argv, owner.logPath)
}

func (m *Manager) recoverSelectedRestart(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) error {
	_, err := m.recoverSelectedRestartWithStart(ctx, host, facts, pending)
	return err
}

func (m *Manager) recoverSelectedRestartWithStart(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) (started bool, err error) {
	started = pending.start
	prior := *pending.selected
	m.applyResolvedTarget(&host)
	if !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch {
		return started, fmt.Errorf("%w: pending host registration changed", errOwnerEvidence)
	}
	report, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return started, err
	}
	if report.ConfigPath != prior.report.ConfigPath || report.HubStateRoot != prior.report.HubStateRoot {
		return started, fmt.Errorf("%w: pending config/root changed", errOwnerEvidence)
	}
	if report.State != "stopped" {
		now, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if err != nil {
			return started, err
		}
		if !sameLaunch(now, prior) {
			return started, fmt.Errorf("%w: pending owner intent changed", errOwnerEvidence)
		}
		if !sameProcess(now, prior) {
			// A successful replacement may have outlived a lost SSH response. Verify
			// that successor instead of needlessly restarting it again.
			if selectedSuccessor(now, prior, pending.expectedVersion, pending.expectedGitSHA) {
				m.clearPendingRestart(host.Name)
				return started, nil
			}
			if err := pending.validateExecutable(host.Name, facts); err != nil {
				return started, err
			}
			return started, fmt.Errorf("%w: %w: pending generation changed without a verified successor", ErrRestart, errOwnerEvidence)
		}
		if !sameOwner(now, prior) {
			return started, fmt.Errorf("%w: pending predecessor changed", errOwnerEvidence)
		}
		if err := pending.validateExecutable(host.Name, facts); err != nil {
			return started, err
		}
		return started, m.restartSelectedPending(ctx, host, facts, pending, true)
	}
	if err := pending.validateExecutable(host.Name, facts); err != nil {
		return started, err
	}
	// Stopped is a resolver-verified free selected lock, not an HTTP timeout.
	// Recheck the service's definition and PID before replay. An ad-hoc replay
	// checks that no newly selected supervisor owns the retained launch.
	if err := m.checkOwnerConfig(ctx, host, prior.argv, prior.cwd, prior.env, report); err != nil {
		return started, err
	}
	if prior.supervisor.kind != supervisorNone {
		if err := m.revalidateStoppedService(ctx, host, facts, prior); err != nil {
			return started, err
		}
	} else if facts.OS == "darwin" {
		defs, err := m.launchdOwners(ctx, host, facts)
		if err != nil {
			return started, err
		}
		for _, def := range defs {
			matches, err := m.launchdOwnerMatches(ctx, host, def, report)
			if err != nil {
				return started, err
			}
			if !matches {
				continue
			}
			return started, fmt.Errorf("%w: stopped launch now supervised", errOwnerEvidence)
		}
	} else {
		sup, err := m.supervisorForOwner(ctx, host, "0", prior.argv, prior.cwd)
		if err != nil {
			return started, err
		}
		if sup.kind != supervisorNone {
			return started, fmt.Errorf("%w: stopped launch now supervised", errOwnerEvidence)
		}
	}
	// Re-resolve immediately before start so another selected lock winner cannot
	// be mistaken for the stopped predecessor. No old listener has to disappear.
	latest, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return started, err
	}
	if latest.State != "stopped" || latest.ConfigPath != report.ConfigPath || latest.HubStateRoot != report.HubStateRoot {
		return started, fmt.Errorf("%w: stopped selection changed before replay", errOwnerEvidence)
	}
	if pending.deployment || pending.command == "" {
		pending.start = true
	}
	started = pending.start
	return started, m.restartSelectedPending(ctx, host, facts, pending, false)
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

// proveSelectedRestartComplete never starts or retries an effect. It permits an
// already-recorded replacement to settle despite a later unusable disk contract,
// but still requires capability and the independent selected successor proof.
func (m *Manager) proveSelectedRestartComplete(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) (selectedOwner, bool, error) {
	if err := m.checkEffectBinding(pending.binding); err != nil {
		return selectedOwner{}, false, err
	}
	now, err := m.resolveOwnedEndpoint(ctx, host, facts)
	if err != nil {
		return selectedOwner{}, false, err
	}
	if !selectedSuccessor(now, *pending.selected, pending.expectedVersion, pending.expectedGitSHA) {
		return selectedOwner{}, false, nil
	}
	if pending.start {
		final, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if err != nil || !selectedSuccessor(final, *pending.selected, pending.expectedVersion, pending.expectedGitSHA) || final.report.Running.StartedAt.IsZero() {
			return selectedOwner{}, false, fmt.Errorf("%w: host %q startup identity could not be verified", ErrRestart, host.Name)
		}
		now = final
	}
	if err := m.checkEffectBinding(pending.binding); err != nil {
		return selectedOwner{}, false, err
	}
	return now, true, nil
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
		if err == nil && selectedSuccessor(now, *pending.selected, pending.expectedVersion, pending.expectedGitSHA) {
			return nil
		}
		if err := m.opts.waitSleep(ctx, restartHealthInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: selected successor PID, generation, boot or build not verified, version %q, want %q, backend_git_sha %q, want %q", ErrRestart, observedVersion, pending.expectedVersion, observedSHA, pending.expectedGitSHA)
}

// A free selected lock and independently absent selected service authorize only
// fresh launch intent, never a guessed listener or replayed command.
func (m *Manager) freshSelectedOwner(ctx context.Context, host hostreg.Host, report hubendpoint.Report) (selectedOwner, error) {
	if addr := explicitHostAddr(m.opts, host); addr != "" {
		if _, err := hubendpoint.ParseListenAddr(addr); err != nil {
			return selectedOwner{}, fmt.Errorf("%w: host %q bootstrap listener: %w", ErrHostAddr, host.Name, err)
		}
	}
	out, err := m.ownerRead(ctx, host, "pwd")
	if err != nil {
		return selectedOwner{}, err
	}
	cwd, err := ownerPathOutput(out)
	if err != nil {
		return selectedOwner{}, errOwnerEvidence
	}
	out, err = m.ownerRead(ctx, host, freshOwnerEnvironmentRemote)
	if err != nil {
		return selectedOwner{}, err
	}
	env, err := decodeOwnerEnvironment(out)
	if err != nil {
		return selectedOwner{}, err
	}
	target, err := m.expectedHubExecutable(ctx, host)
	if err != nil {
		return selectedOwner{}, err
	}
	argv := hubBootstrapArgv(m.opts, host, target)
	physicalCwd, err := m.directoryIdentity(ctx, host, cwd)
	if err != nil {
		return selectedOwner{}, err
	}
	owner := selectedOwner{host: host, pid: "0", report: report, cwd: cwd, cwdIdentity: physicalCwd, env: env, argv: argv}
	if err := m.checkOwnerConfig(ctx, host, argv, cwd, env, report); err != nil {
		return selectedOwner{}, err
	}
	return owner, nil
}

const freshOwnerEnvironmentRemote = `if [ "${HOME+x}" = x ]; then printf '%s\0' "HOME=$HOME"; fi; if [ "${XDG_CONFIG_HOME+x}" = x ]; then printf '%s\0' "XDG_CONFIG_HOME=$XDG_CONFIG_HOME"; fi; if [ "${XDG_STATE_HOME+x}" = x ]; then printf '%s\0' "XDG_STATE_HOME=$XDG_STATE_HOME"; fi; if [ "${EVENER_HUB_LISTEN_ADDR+x}" = x ]; then printf '%s\0' "EVENER_HUB_LISTEN_ADDR=$EVENER_HUB_LISTEN_ADDR"; fi`
