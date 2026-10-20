package sshconn

// The 04b deploy/restart entry points the deploy pipeline's operation workers
// call (deploy pipeline 08b §6). The hub's `deploy` and `restart` handlers own
// admission, the token, dedup and the durable record; once the record exists
// its worker runs these. They are deliberately the same code paths `Ensure`
// drives — the same guards, the same refusals, the same resolved-target
// persistence — with the caller holding the host's per-host gate instead of the
// attach ladder (Manager.TryAcquire is the gate).
//
// Neither entry point acquires the gate: the caller already holds it (the gate
// is the same non-reentrant per-host lock), and re-acquiring it would deadlock.

import (
	"context"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// DeployForOperation runs the 04b deploy path against host under the caller's
// held gate and returns the resolved install target plus the launch-contract
// facts re-read after the install. It performs exactly what ensureOnce's deploy
// step plus its immediate guards perform:
//
//   - `m.deploy` itself: the terminal dirty-controller refusal, source and
//     revision verification, the push's atomic temp-plus-rename install with
//     its byte-count integrity check (or the pinned installer fallback), and
//     the resolved-target return;
//   - the resolved target is recorded as host.EvenerPath and remembered across
//     attempts (setResolvedTarget), so the restart, health verification and
//     later attaches address the binary that was actually installed;
//   - the launch contract is re-read on disk and judged (deployedBuildNotStamped):
//     an artifact that is not this controller's build is the terminal
//     unstamped-build refusal, exactly as in the attach ladder.
//
// The phase is bounded by the deploy limit, and the caller's context (the
// operation worker's controller-lifetime context) cancels it.
func (m *Manager) DeployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, restart bool) (string, Preflight, error) {
	return m.deployForOperation(ctx, host, facts, m.deploymentIntent(restart, hubIdentity{}))
}

func (m *Manager) deployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, intent pendingRestartState) (string, Preflight, error) {
	deployCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	beforeWrite := func(target string) {
		if intent.restart && intent.replaced.startedAt.IsZero() && intent.command == "" {
			intent.replaced, _ = m.probeRunningHub(deployCtx, host)
		}
		m.deploymentRecorder(host.Name, intent)(target)
	}
	target, err := m.deploy(deployCtx, host, facts, beforeWrite)
	if err != nil {
		return "", facts, err
	}
	m.acknowledgeDeployment(host.Name)
	if target != "" {
		host.EvenerPath = target
		m.setResolvedTarget(host.Name, target)
	}
	after, err := m.reReadLaunchContract(deployCtx, host, facts)
	if err != nil {
		return "", facts, err
	}
	if err := m.deployedBuildNotStamped(host.Name, after, intent.expectedVersion); err != nil {
		return "", facts, err
	}
	if !intent.restart {
		m.clearPendingRestart(host.Name)
	}
	return target, after, nil
}

// RestartForOperation runs the 04b restart path against host under the caller's
// held gate: the same user-versus-system unit decision and proven-replacement
// wait `ensureOnce`'s restart step runs (restartHub), including the recorded
// restart a failed attempt leaves for the next Ensure. The phase is bounded by
// the deploy limit.
//
// The hub's restart worker owns the post-restart reattach and re-probe
// (deploy pipeline §6's post-operation refresh); this call ends once the
// replacement is proven healthy. It returns the selected target version for
// that refresh, which may differ from the predecessor's version when this
// restart completes a pending deployment. The target is captured before the
// restart, never inferred from the replacement's health response.
func (m *Manager) RestartForOperation(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	if pending := m.pendingRestart(host.Name); pending.needsDeployment(facts) {
		// This explicit Restart authorizes replacement even when the unfinished
		// deployment was deploy-only. Finish its write before replacing its marker.
		pending.restart = true
		_, after, err := m.deployForOperation(ctx, host, facts, pending)
		if err != nil {
			return "", err
		}
		facts = after
		m.applyResolvedTarget(&host)
	}
	restartCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	m.applyResolvedTarget(&host)
	// Attachment proves the running hub's protocol, not the executable that
	// will replace it. Re-read that target before stopping a usable process.
	facts, err := m.reReadLaunchContract(restartCtx, host, facts)
	if err != nil {
		return "", err
	}
	running, _ := m.probeRunningHub(restartCtx, host)
	version, pin := expectedServedBuild(facts, m.opts.controllerVersion())
	if pending := m.pendingRestart(host.Name); pending.active() {
		version, pin = pending.expectedVersion, pending.expectedGitSHA
		running = pending.restartPredecessor(running)
	}
	if err := m.restartHub(restartCtx, host, facts, version, pin, running); err != nil {
		return version, err
	}
	// Health has proved the replacement. The predecessor's SSH reader may not
	// have observed EOF yet, so make reattach replace its channel immediately.
	if ch := m.currentChannel(host.Name); ch != nil && ch.MatchesRegistration(host) {
		ch.markLost()
	}
	return version, nil
}
