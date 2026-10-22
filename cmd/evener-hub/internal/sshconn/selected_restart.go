package sshconn

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

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

func (m *Manager) selectedRelaunchTarget(ctx context.Context, host hostreg.Host, owner selectedOwner) (string, error) {
	executable := owner.argv[0]
	if !path.IsAbs(executable) && strings.Contains(executable, "/") {
		executable = owner.cwd + "/" + executable
	}
	executable, err := m.resolveExecutableName(ctx, host, executable)
	if err != nil {
		return "", err
	}
	return m.resolveRemotePath(ctx, host, executable)
}

// checkSelectedReplacement is the last read-only boundary before any selected
// stop or start. A lock holder is a locator, never stop authority. Both the live
// predecessor and the definition that will launch its replacement must agree.
func (m *Manager) checkSelectedReplacement(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, live bool, nextEnv []string) ([]string, error) {
	if live {
		retained := owner.report.Running
		read := m.resolveHostEndpoint
		if retained != nil && retained.BootID != "" {
			read = func(ctx context.Context, host hostreg.Host, facts Preflight) (hubendpoint.Report, error) {
				report, err := m.resolveHostEndpoint(ctx, host, facts)
				if errors.Is(err, ErrPreflightDecode) {
					return report, err // Invalid or mismatched identity is not unavailable health.
				}
				if err != nil {
					// Retained config/root are locators, not health or stop authority.
					// Independent process, launch, config and capability checks below
					// must still succeed even when this endpoint read fails.
					report = owner.clone().report
					report.State, report.Running = "unavailable", nil
				}
				running := report.Running
				if running != nil && (running.BootID != retained.BootID || running.PID != retained.PID || strconv.Itoa(running.PID) != owner.pid || running.ConfigPath != retained.ConfigPath || running.HubStateRoot != retained.HubStateRoot || report.ConfigPath != owner.report.ConfigPath || report.HubStateRoot != owner.report.HubStateRoot) {
					return report, fmt.Errorf("%w: stop requires the matching live report", errOwnerEvidence)
				}
				return report, nil
			}
		} else {
			// Missing health is not missing ownership. Reprove the same process,
			// launch, supervisor, config and original-image capability below.
			// An absent boot cannot constrain a newly available health report.
			owner.report.Running = nil
		}
		_, err := m.revalidateOwnedEndpointUsing(ctx, host, facts, owner, read)
		if err != nil {
			return nil, err
		}
	}
	if !live {
		cwd, err := m.directoryIdentity(ctx, host, owner.cwd)
		if err != nil {
			return nil, err
		}
		if cwd != owner.cwdIdentity {
			return nil, fmt.Errorf("%w: stopped working directory changed", errOwnerEvidence)
		}
	}
	if owner.supervisor.kind == supervisorLaunchd {
		if facts.UID != owner.domainUID {
			return nil, fmt.Errorf("%w: selected launchd domain changed", errOwnerEvidence)
		}
		defs, err := m.launchdOwners(ctx, host, facts)
		if err != nil {
			return nil, err
		}
		def, ok := defs[owner.supervisor]
		pid := "0"
		if live {
			pid = owner.pid
		}
		if !ok || def.pid != pid || launchdIntent(def) != owner.definition {
			return nil, fmt.Errorf("%w: selected launchd definition changed", errOwnerEvidence)
		}
		return owner.env, m.checkOwnerConfig(ctx, host, def.argv, def.cwd, def.env, owner.report)
	}
	if owner.supervisor.kind != supervisorNone {
		pid := "0"
		if live {
			pid = owner.pid
		}
		sup, _, err := m.supervisorForOwner(ctx, host, pid, owner.argv, owner.cwd, owner.env, owner.report, owner.supervisor)
		if err != nil {
			return nil, err
		}
		if sup != owner.supervisor {
			return nil, fmt.Errorf("%w: selected service changed", errOwnerEvidence)
		}
		definition, err := m.serviceDefinition(ctx, host, sup)
		if err != nil {
			return nil, err
		}
		if definition != owner.definition {
			return nil, fmt.Errorf("%w: selected service definition changed", errOwnerEvidence)
		}
		user, ok := effectiveUserName(host)
		if !ok {
			out, err := m.ownerRead(ctx, host, "id -un")
			if err != nil {
				return nil, err
			}
			user = firstLine(string(out))
		}
		env, err := m.stoppedServiceEnvironment(ctx, host, sup, user)
		if err != nil {
			return nil, err
		}
		if !live {
			if nextEnv == nil {
				nextEnv = owner.env
			}
			if !equalStrings(env, nextEnv) {
				return nil, fmt.Errorf("%w: next service environment disagrees with selection", errOwnerEvidence)
			}
			env = nextEnv
		}
		return env, m.checkOwnerConfig(ctx, host, owner.launchArguments(), owner.cwd, env, owner.report)
	}
	return owner.env, m.checkOwnerConfig(ctx, host, owner.argv, owner.cwd, owner.env, owner.report)
}

func (m *Manager) recoverSelectedRestart(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) error {
	_, err := m.recoverSelectedRestartWithStart(ctx, host, facts, &pending, false)
	return err
}

func checkSelectedRestartRegistration(host, prior hostreg.Host) error {
	if !host.Equal(prior) || host.Generation != prior.Generation || host.IncarnationID != prior.IncarnationID || host.PresenceEpoch != prior.PresenceEpoch {
		return fmt.Errorf("%w: pending host registration changed", errOwnerEvidence)
	}
	return nil
}

func (m *Manager) recoverSelectedRestartWithStart(ctx context.Context, host hostreg.Host, facts Preflight, pending *pendingRestartState, retryUnhealthySuccessor bool) (started bool, err error) {
	started = pending.start
	prior := *pending.selected
	m.applyResolvedTarget(&host)
	if err := checkSelectedRestartRegistration(host, prior.host); err != nil {
		return started, err
	}
	report, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return started, err
	}
	if report.ConfigPath != prior.report.ConfigPath || report.HubStateRoot != prior.report.HubStateRoot {
		return started, fmt.Errorf("%w: pending config/root changed", errOwnerEvidence)
	}
	stopped := report.State == "stopped"
	if !stopped && report.Running == nil {
		stopped = prior.pid == "0"
		if !stopped {
			out, err := m.ownerRead(ctx, host, processAliveRemote(prior.pid))
			if err != nil {
				return started, err
			}
			stopped = strings.TrimSpace(string(out)) == pidGoneMarker
		}
	}
	if stopped && retryUnhealthySuccessor && pending.nextEnv != nil && report.State != "stopped" {
		// A gone predecessor does not imply a stopped installation: its replacement
		// may own the root without answering health. Only a fresh explicit Restart
		// may replace that independently verified successor, never an early proof
		// of completion or an automatic reconnect.
		now, resolveErr := m.resolveOwnedEndpoint(ctx, host, facts)
		if resolveErr == nil && now.pid != "0" && !sameProcess(now, prior) && now.report.Running == nil {
			return started, m.retryUnhealthySelectedSuccessor(ctx, host, facts, pending, now)
		}
	}
	if !stopped {
		now, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if err != nil {
			return started, err
		}
		if sameProcess(now, prior) && !sameLaunch(now, prior) {
			return started, fmt.Errorf("%w: pending owner intent changed", errOwnerEvidence)
		}
		if !sameProcess(now, prior) {
			env := pending.nextEnv
			if env == nil {
				env = prior.env
			}
			if !sameLaunchWithEnv(now, prior, env) {
				return started, fmt.Errorf("%w: pending owner intent changed", errOwnerEvidence)
			}
			if retryUnhealthySuccessor && pending.nextEnv != nil && now.report.Running == nil {
				return started, m.retryUnhealthySelectedSuccessor(ctx, host, facts, pending, now)
			}
			// A successful replacement may have outlived a lost SSH response. Verify
			// that successor instead of needlessly restarting it again.
			if selectedSuccessor(now, prior, pending.nextEnv, pending.expectedVersion, pending.expectedGitSHA) {
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
		if prior.report.Running == nil && now.report.Running != nil {
			// Only the independently matched predecessor may fill missing health
			// evidence. Preserve launch intent and any previously retained boot.
			refreshed := prior.clone()
			refreshed.report = now.clone().report
			pending.selected = &refreshed
		}
		return started, m.restartSelectedPending(ctx, host, facts, pending, true)
	}
	if err := pending.validateExecutable(host.Name, facts); err != nil {
		return started, err
	}
	// An absent process permits only a non-stopping start, not a restart.
	// Recheck the service's definition and PID before replay. An ad-hoc replay
	// checks that no newly selected supervisor owns the retained launch.
	env := pending.nextEnv
	if env == nil {
		env = prior.env
	}
	if err := m.checkOwnerConfig(ctx, host, prior.launchArguments(), prior.cwd, env, report); err != nil {
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
		sup, _, err := m.supervisorForOwner(ctx, host, "0", prior.argv, prior.cwd, prior.env, prior.report)
		if err != nil {
			return started, err
		}
		if sup.kind != supervisorNone {
			return started, fmt.Errorf("%w: stopped launch now supervised", errOwnerEvidence)
		}
	}
	// Re-resolve the next launch identity, not remote lock availability. Another
	// root owner is protected by the new process's own hub.lock acquisition.
	latest, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return started, err
	}
	if latest.Running != nil || latest.ConfigPath != report.ConfigPath || latest.HubStateRoot != report.HubStateRoot {
		return started, fmt.Errorf("%w: stopped selection changed before replay", errOwnerEvidence)
	}
	if pending.deployment || pending.command == "" {
		pending.start = true
	}
	started = pending.start
	return started, m.restartSelectedPending(ctx, host, facts, pending, false)
}

func (m *Manager) retryUnhealthySelectedSuccessor(ctx context.Context, host hostreg.Host, facts Preflight, pending *pendingRestartState, now selectedOwner) error {
	if err := checkSelectedRestartRegistration(now.host, pending.selected.host); err != nil {
		return err
	}
	if !sameLaunchWithEnv(now, *pending.selected, pending.nextEnv) {
		return fmt.Errorf("%w: unhealthy successor launch changed", errOwnerEvidence)
	}
	if err := pending.validateExecutable(host.Name, facts); err != nil {
		return err
	}
	// launch-check reports buildinfo.Version, the full GitSHA with an optional
	// dirty suffix. A distinct retained SHA pin must agree with that same image.
	if pending.expectedGitSHA != "" && strings.TrimSuffix(pending.expectedVersion, "-dirty") != pending.expectedGitSHA {
		return fmt.Errorf("%w: unhealthy successor build pin unavailable", errOwnerEvidence)
	}
	var err error
	if facts.OS == "darwin" {
		err = m.probeDarwinImageBuild(ctx, host, now.pid, facts, pending.expectedVersion)
	} else {
		err = m.probeOwnerImageBuild(ctx, host, now.pid, pending.expectedVersion)
	}
	if err != nil {
		return err
	}
	// Bracket the image probe with independent same-owner checks, then repeat
	// the ordinary replacement gate immediately before the stop/start command.
	intent, err := m.selectedRestartIntentUsing(ctx, host, facts, now, pending.expectedVersion, pending.expectedGitSHA, m.resolveHostEndpoint)
	if err != nil {
		return err
	}
	previous := *pending
	pending.selected, pending.command = intent.selected, intent.command
	return m.restartPreparedSelectedPending(ctx, host, facts, pending, true, previous)
}

func (m *Manager) revalidateStoppedService(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner) error {
	if owner.supervisor.kind == supervisorLaunchd {
		return m.revalidateStoppedLaunchd(ctx, host, facts, owner)
	}
	sup, _, err := m.supervisorForOwner(ctx, host, "0", owner.argv, owner.cwd, owner.env, owner.report, owner.supervisor)
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

func selectedSuccessor(now, prior selectedOwner, env []string, version, pin string) bool {
	// Without a predecessor report, only a validated replacement gate can
	// establish successor intent. Check before legacy fallback and early proofs.
	if prior.pid != "0" && prior.report.Running == nil && env == nil {
		return false
	}
	if env == nil {
		env = prior.env
	}
	running := now.report.Running
	return running != nil && !sameProcess(now, prior) && sameLaunchWithEnv(now, prior, env) && running.Version == version && hubBuildSHAMatches(running.BackendGitSHA, pin) && (prior.report.Running == nil || running.BootID != prior.report.Running.BootID)
}

// proveSelectedRestartComplete never starts or retries an effect. It permits an
// already-recorded replacement to settle despite a later unusable disk contract,
// but still requires capability and the independent selected successor proof.
// Callers must first check the retained registration, even without an effect binding.
func (m *Manager) proveSelectedRestartComplete(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState) (selectedOwner, bool, error) {
	if err := m.checkEffectBinding(pending.binding); err != nil {
		return selectedOwner{}, false, err
	}
	now, err := m.resolveOwnedEndpoint(ctx, host, facts)
	if err != nil {
		return selectedOwner{}, false, err
	}
	if !selectedSuccessor(now, *pending.selected, pending.nextEnv, pending.expectedVersion, pending.expectedGitSHA) {
		return selectedOwner{}, false, nil
	}
	if pending.start || pending.replaced.startedAt.IsZero() {
		final, err := m.resolveOwnedEndpoint(ctx, host, facts)
		if err != nil || !selectedSuccessor(final, *pending.selected, pending.nextEnv, pending.expectedVersion, pending.expectedGitSHA) || final.report.Running.StartedAt.IsZero() {
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
		if err == nil && selectedSuccessor(now, *pending.selected, pending.nextEnv, pending.expectedVersion, pending.expectedGitSHA) {
			return nil
		}
		if err := m.opts.waitSleep(ctx, restartHealthInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: selected successor PID, generation, boot or build not verified, version %q, want %q, backend_git_sha %q, want %q", ErrRestart, observedVersion, pending.expectedVersion, observedSHA, pending.expectedGitSHA)
}

// An independently absent selected service permits only fresh launch intent,
// never a guessed listener or replayed command. The new process acquires its lock.
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
